package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"dmesh/internal/auth"
	"dmesh/internal/meshauth"
	"dmesh/internal/store"
)

type ctxKey string

const claimsKey ctxKey = "claims"

type otpRequest struct {
	Email string `json:"email"`
}

// requestOTP emails a single-use code. OTP exists for the dashboard only;
// node registration and settlement use Stellar key signatures.
func (s *Server) requestOTP(w http.ResponseWriter, r *http.Request) {
	var in otpRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	email := auth.NormalizeEmail(in.Email)
	if email == "" || !strings.Contains(email, "@") {
		writeError(w, http.StatusBadRequest, errors.New("valid email required"))
		return
	}
	ip := clientIP(r).String()
	if !s.ipLimiter.allow(ip) || !s.limiter.allow(email) {
		writeError(w, http.StatusTooManyRequests, errors.New("too many OTP requests, try later"))
		return
	}
	code, err := auth.NewOTP()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.Store.SaveAuthCode(email, auth.HashCode(code, s.Cfg.JWTSecret),
		time.Now().UTC().Add(s.Cfg.OTPTTL)); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	subject := "Your dmesh dashboard code"
	body := "Your dmesh login code is " + code + ". It expires in " +
		strings.TrimSuffix(s.Cfg.OTPTTL.String(), "0s") + "."
	if s.Sender == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("email delivery is not configured"))
		return
	}
	if err := s.Sender.Send(email, subject, body); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if s.Cfg.DevCodes {
		s.devCodes[email] = code
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"email":      email,
		"expires_in": int(s.Cfg.OTPTTL.Seconds()),
	})
}

type otpVerify struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

func (s *Server) verifyOTP(w http.ResponseWriter, r *http.Request) {
	var in otpVerify
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	email := auth.NormalizeEmail(in.Email)
	ok, err := s.Store.ConsumeAuthCode(email, auth.HashCode(strings.TrimSpace(in.Code), s.Cfg.JWTSecret))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, errors.New("invalid or expired code"))
		return
	}
	token, err := auth.MakeToken(0, email, s.Cfg.AdminEmails[email], s.Cfg.JWTSecret, s.Cfg.TokenTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "email": email, "admin": s.Cfg.AdminEmails[email]})
}

// debugOTP exposes the latest dev code; route is registered only in dev mode.
func (s *Server) debugOTP(w http.ResponseWriter, r *http.Request) {
	email := auth.NormalizeEmail(r.URL.Query().Get("email"))
	code, ok := s.devCodes[email]
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("no dev code for this email"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"email": email, "code": code})
}

type linkRequest struct {
	MeshAddress string `json:"mesh_address"`
}

// linkNode connects a dashboard email to a node the requester proves they
// control by signing the request with that node's Stellar key.
func (s *Server) linkNode(w http.ResponseWriter, r *http.Request) {
	claims, ok := r.Context().Value(claimsKey).(*auth.Claims)
	if !ok {
		writeError(w, http.StatusUnauthorized, errors.New("missing dashboard session"))
		return
	}
	var in linkRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if _, err := meshauth.ParseSigned(
		r.Header.Get(meshauth.HeaderAddress),
		r.Header.Get(meshauth.HeaderTimestamp),
		r.Header.Get(meshauth.HeaderSignature),
		"link", in.MeshAddress); err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	if r.Header.Get(meshauth.HeaderAddress) != in.MeshAddress {
		writeError(w, http.StatusBadRequest, errors.New("signed address does not match mesh_address"))
		return
	}
	if _, err := s.Store.NodeByMeshAddress(in.MeshAddress); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, errors.New("register this node before linking it"))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.Store.LinkNodeEmail(in.MeshAddress, claims.Email); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) dashboardNodes(w http.ResponseWriter, r *http.Request) {
	claims, ok := r.Context().Value(claimsKey).(*auth.Claims)
	if !ok {
		writeError(w, http.StatusUnauthorized, errors.New("missing dashboard session"))
		return
	}
	nodes, err := s.Store.NodesByEmail(claims.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}

func (s *Server) dashboardRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			writeError(w, http.StatusUnauthorized, errors.New("bearer token required"))
			return
		}
		claims, err := auth.ParseToken(strings.TrimPrefix(header, "Bearer "), s.Cfg.JWTSecret)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}
		ctx := context.WithValue(r.Context(), claimsKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
