package api

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"

	"dmesh/internal/meshauth"
	"dmesh/internal/store"
)

// Input bounds for the self-attested node record. The coordinator is a dumb
// relay: the node client resolves its own geography from the CI-published geo
// database and signs the result; the server only validates shape and stores it.
const (
	maxPayoutAddressLen = 120
	maxCountryISOLen    = 8
	maxCountryNameLen   = 80
	maxASNOrgLen        = 120
	maxEndpointLen      = 255 // host:port, IPv6 included
	maxNATTypeLen       = 16
	maxConnectivityLen  = 20
)

// allowedNATTypes/allowedConnectivity bound the self-probed reachability
// vocabulary; unknown values are rejected rather than stored.
var allowedNATTypes = map[string]bool{
	"public": true, "cone": true, "symmetric": true,
	"udp-blocked": true, "probe-failed": true, "": true,
}
var allowedConnectivity = map[string]bool{
	"direct": true, "hole-punch": true, "relay-required": true, "unknown": true, "": true,
}

// allowedTiers are the two pricing classes the contract understands.
var allowedTiers = map[string]bool{"res": true, "dc": true}

type registerRequest struct {
	// Geography and tier are computed by the node client locally (using the
	// geo database published by CI) and covered by the Stellar signature, so a
	// record cannot be altered in transit. The coordinator never looks an IP up.
	Tier          string `json:"tier"`
	CountryISO    string `json:"country_iso"`
	CountryName   string `json:"country"`
	ASN           uint   `json:"asn"`
	ASNOrg        string `json:"asn_org"`
	PayoutAddress string `json:"payout_address"`
	// Self-probed STUN reachability. The endpoint is the operator-published
	// dial address; an empty endpoint means the node is not directly reachable.
	Endpoint     string `json:"endpoint"`
	NATType      string `json:"nat_type"`
	Connectivity string `json:"connectivity"`
	CGNAT        bool   `json:"cgnat"`
}

// registerNode registers a mesh node. The only identity is the Stellar G
// address. The node signs its own geo/tier claim; the coordinator stores the
// signed record and the transport IP (kept private) for abuse control only.
func (s *Server) registerNode(w http.ResponseWriter, r *http.Request) {
	var in registerRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<13)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	in.Tier = strings.TrimSpace(in.Tier)
	in.CountryISO = strings.TrimSpace(in.CountryISO)
	in.CountryName = strings.TrimSpace(in.CountryName)
	in.ASNOrg = strings.TrimSpace(in.ASNOrg)
	in.PayoutAddress = strings.TrimSpace(in.PayoutAddress)
	in.Endpoint = strings.TrimSpace(in.Endpoint)
	in.NATType = strings.TrimSpace(in.NATType)
	in.Connectivity = strings.TrimSpace(in.Connectivity)

	if !allowedTiers[in.Tier] {
		writeError(w, http.StatusBadRequest, errTierInvalid)
		return
	}
	if len(in.CountryISO) > maxCountryISOLen || len(in.CountryName) > maxCountryNameLen || len(in.ASNOrg) > maxASNOrgLen {
		writeError(w, http.StatusBadRequest, errGeoFieldTooLong)
		return
	}
	if len(in.PayoutAddress) > maxPayoutAddressLen {
		writeError(w, http.StatusBadRequest, errPayoutTooLong)
		return
	}
	if in.PayoutAddress != "" && !isSafeAddress(in.PayoutAddress) {
		writeError(w, http.StatusBadRequest, errPayoutBadChars)
		return
	}
	if len(in.Endpoint) > maxEndpointLen || len(in.NATType) > maxNATTypeLen || len(in.Connectivity) > maxConnectivityLen {
		writeError(w, http.StatusBadRequest, errors.New("reachability field too long"))
		return
	}
	if !allowedNATTypes[in.NATType] || !allowedConnectivity[in.Connectivity] {
		writeError(w, http.StatusBadRequest, errors.New("unknown nat_type or connectivity value"))
		return
	}
	if in.Endpoint != "" {
		if host, _, err := net.SplitHostPort(in.Endpoint); err != nil || host == "" {
			writeError(w, http.StatusBadRequest, errors.New("endpoint must be host:port"))
			return
		}
	}

	// Every claimed field participates in the canonical signed payload, so the
	// stored record is exactly what the node's own key attested to.
	parts := []string{
		"register",
		in.Tier,
		in.CountryISO,
		in.CountryName,
		strconv.FormatUint(uint64(in.ASN), 10),
		in.ASNOrg,
		in.PayoutAddress,
		in.Endpoint,
		in.NATType,
		in.Connectivity,
		strconv.FormatBool(in.CGNAT),
	}
	req, err := meshauth.ParseSigned(
		r.Header.Get(meshauth.HeaderAddress),
		r.Header.Get(meshauth.HeaderTimestamp),
		r.Header.Get(meshauth.HeaderSignature),
		parts...)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}

	// Transport IP is retained privately for rate limiting/abuse handling and
	// is never serialized to the public directory.
	node, err := s.Store.UpsertNode(&store.Node{
		MeshAddress:   req.Address,
		PayoutAddress: in.PayoutAddress,
		CountryISO:    in.CountryISO,
		CountryName:   in.CountryName,
		ASN:           in.ASN,
		ASNOrg:        in.ASNOrg,
		Tier:          in.Tier,
		IP:            clientIP(r).String(),
		Endpoint:      in.Endpoint,
		NATType:       in.NATType,
		Connectivity:  in.Connectivity,
		CGNAT:         in.CGNAT,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"node":        node,
		"tier_source": "client-signed",
	})
}

type heartbeatRequest struct {
	UpBytes   int64 `json:"up_bytes"`
	DownBytes int64 `json:"down_bytes"`
}

// nodeHeartbeat keeps a node online and refreshes its cumulative counters.
func (s *Server) nodeHeartbeat(w http.ResponseWriter, r *http.Request) {
	var in heartbeatRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req, err := meshauth.ParseSigned(
		r.Header.Get(meshauth.HeaderAddress),
		r.Header.Get(meshauth.HeaderTimestamp),
		r.Header.Get(meshauth.HeaderSignature),
		"heartbeat")
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	if err := s.Store.Heartbeat(req.Address, in.UpBytes, in.DownBytes); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "online"})
}

// echoIP reports the transport IP the request came from. It performs no
// classification: the node is expected to derive its own tier/geography
// locally against the CI-published geo database.
func (s *Server) echoIP(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ip": clientIP(r).String()})
}

// listNodes is the public node directory. Raw peer IPs are never exposed.
func (s *Server) listNodes(w http.ResponseWriter, _ *http.Request) {
	nodes, err := s.Store.ListOnlineNodes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	public := make([]map[string]any, 0, len(nodes))
	for _, n := range nodes {
		public = append(public, map[string]any{
			"mesh_address":   n.MeshAddress,
			"payout_address": n.PayoutAddress,
			"country_iso":    n.CountryISO,
			"country":        n.CountryName,
			"tier":           n.Tier,
			"asn_org":        n.ASNOrg,
			"is_datacenter":  n.Tier == "dc",
			// Operator-published dial information; the coordinator-observed
			// transport IP remains private.
			"endpoint":     n.Endpoint,
			"nat_type":     n.NATType,
			"connectivity": n.Connectivity,
			"cgnat":        n.CGNAT,
			"last_seen":    n.LastSeen,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": public})
}
