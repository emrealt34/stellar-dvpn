package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"dmesh/internal/meshauth"
	"dmesh/internal/store"
)

type coordinateRequest struct {
	NodeAddress  string `json:"node_address"`
	TokenAddress string `json:"token_address"`
	TTLSeconds   uint64 `json:"ttl_seconds"`
}

// coordinateSession issues the nonce and parameters a client needs to call
// the contract's open_session. The coordinator never signs or submits that
// transaction: the client does, locally.
func (s *Server) coordinateSession(w http.ResponseWriter, r *http.Request) {
	var in coordinateRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req, err := meshauth.ParseSigned(
		r.Header.Get(meshauth.HeaderAddress),
		r.Header.Get(meshauth.HeaderTimestamp),
		r.Header.Get(meshauth.HeaderSignature),
		"open", in.NodeAddress, in.TokenAddress)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	node, err := s.Store.NodeByMeshAddress(in.NodeAddress)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errors.New("unknown node address"))
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if node.Status != "online" {
		writeError(w, http.StatusConflict, errors.New("node is offline"))
		return
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	ttl := s.Cfg.SessionDefaultTTL
	if in.TTLSeconds > 0 {
		ttl = time.Duration(in.TTLSeconds) * time.Second
		if ttl > 24*time.Hour {
			writeError(w, http.StatusBadRequest, errors.New("ttl_seconds exceeds 24h maximum"))
			return
		}
	}
	pending, err := s.Store.CreatePendingSession(&store.Session{
		ClientAddress: req.Address,
		NodeAddress:   in.NodeAddress,
		TokenAddress:  in.TokenAddress,
		Tier:          node.Tier,
		NonceHex:      hex.EncodeToString(nonce[:]),
		ExpiresAt:     time.Now().UTC().Add(ttl),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"session": pending,
		"open_session_args": map[string]any{
			"client":      req.Address,
			"node":        in.NodeAddress,
			"token":       in.TokenAddress,
			"tier":        node.Tier,
			"nonce_hex":   pending.NonceHex,
			"ttl_seconds": uint64(ttl.Seconds()),
			"contract_id": s.Cfg.SettlementContractID,
			"network":     s.Cfg.NetworkPassphrase,
			"rpc_url":     s.Cfg.SorobanRPCURL,
		},
		"next_step": "approve the token allowance, then sign and submit open_session locally",
	})
}

type onChainRequest struct {
	NonceHex          string `json:"nonce_hex"`
	ContractSessionID string `json:"contract_session_id"`
	TxHash            string `json:"tx_hash"`
}

// attachOnChain records the on-chain identifiers of a client-signed open_session.
func (s *Server) attachOnChain(w http.ResponseWriter, r *http.Request) {
	var in onChainRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req, err := meshauth.ParseSigned(
		r.Header.Get(meshauth.HeaderAddress),
		r.Header.Get(meshauth.HeaderTimestamp),
		r.Header.Get(meshauth.HeaderSignature),
		"attach", in.NonceHex, in.TxHash)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	session, err := s.Store.SessionByNonce(in.NonceHex)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errors.New("unknown session nonce"))
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if session.ClientAddress != req.Address {
		writeError(w, http.StatusForbidden, errors.New("only the session client can attach its open tx"))
		return
	}
	if in.ContractSessionID == "" || in.TxHash == "" {
		writeError(w, http.StatusBadRequest, errors.New("contract_session_id and tx_hash are required"))
		return
	}
	if err := s.Store.AttachOpenTx(session.ID, in.ContractSessionID, in.TxHash); err != nil {
		if errors.Is(err, store.ErrSessionNotAttachable) {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type countersRequest struct {
	NonceHex  string `json:"nonce_hex"`
	UpBytes   int64  `json:"up_bytes"`
	DownBytes int64  `json:"down_bytes"`
}

// submitCounters accepts a signed counter report from either party.
func (s *Server) submitCounters(w http.ResponseWriter, r *http.Request) {
	var in countersRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req, err := meshauth.ParseSigned(
		r.Header.Get(meshauth.HeaderAddress),
		r.Header.Get(meshauth.HeaderTimestamp),
		r.Header.Get(meshauth.HeaderSignature),
		"counters", in.NonceHex)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	session, err := s.Store.SessionByNonce(in.NonceHex)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errors.New("unknown session nonce"))
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	record, err := s.Metering.Submit(session, req.Address, in.UpBytes, in.DownBytes)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, record)
}

type settlementRequest struct {
	NonceHex  string `json:"nonce_hex"`
	Status    string `json:"status"` // proposed | settled | cancelled
	TxHash    string `json:"tx_hash"`
	UpBytes   int64  `json:"up_bytes"`
	DownBytes int64  `json:"down_bytes"`
}

// recordSettlement mirrors an on-chain lifecycle transition in the
// coordinator database. The chain remains the source of truth; this only
// keeps the dashboard and directory in sync.
func (s *Server) recordSettlement(w http.ResponseWriter, r *http.Request) {
	var in settlementRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	switch in.Status {
	case "proposed", "settled", "cancelled":
	default:
		writeError(w, http.StatusBadRequest, errors.New("status must be proposed, settled or cancelled"))
		return
	}
	req, err := meshauth.ParseSigned(
		r.Header.Get(meshauth.HeaderAddress),
		r.Header.Get(meshauth.HeaderTimestamp),
		r.Header.Get(meshauth.HeaderSignature),
		"settlement", in.NonceHex, in.Status)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	session, err := s.Store.SessionByNonce(in.NonceHex)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errors.New("unknown session nonce"))
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if req.Address != session.ClientAddress && req.Address != session.NodeAddress {
		writeError(w, http.StatusForbidden, errors.New("signer is not a session party"))
		return
	}
	if err := s.Store.SetSessionStatus(session.ID, in.Status, in.TxHash, in.UpBytes, in.DownBytes); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listSessions returns every session where the signer is client or node.
func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	req, err := meshauth.ParseSigned(
		r.Header.Get(meshauth.HeaderAddress),
		r.Header.Get(meshauth.HeaderTimestamp),
		r.Header.Get(meshauth.HeaderSignature),
		"list")
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	sessions, err := s.Store.ListSessionsByParty(req.Address, 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

// lookupSession lets a tunnel endpoint verify a session's state and parties
// before accepting a peer-to-peer connection. The signer must be one of the
// two session participants; the response carries no traffic payload.
func (s *Server) lookupSession(w http.ResponseWriter, r *http.Request) {
	nonce := r.URL.Query().Get("nonce")
	if len(nonce) != 64 {
		writeError(w, http.StatusBadRequest, errors.New("nonce query parameter (32-byte hex) required"))
		return
	}
	req, err := meshauth.ParseSigned(
		r.Header.Get(meshauth.HeaderAddress),
		r.Header.Get(meshauth.HeaderTimestamp),
		r.Header.Get(meshauth.HeaderSignature),
		"GET", "session-lookup", nonce)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	session, err := s.Store.SessionByNonce(nonce)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errors.New("unknown session"))
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if req.Address != session.ClientAddress && req.Address != session.NodeAddress {
		writeError(w, http.StatusForbidden, errors.New("signer is not a session participant"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"nonce":          session.NonceHex,
		"status":         session.Status,
		"client_address": session.ClientAddress,
		"node_address":   session.NodeAddress,
		"tier":           session.Tier,
		"expires_at":     session.ExpiresAt.UTC().Format(time.RFC3339),
	})
}
