package api

import (
	"net/http"

	"dmesh/internal/geo"
	"dmesh/internal/meshauth"
	"dmesh/internal/store"
)

// registerNode registers a mesh node. The only identity is the Stellar G
// address; the geo classification (country, residential/datacenter tier) is
// derived here from the peer IP using the CI-refreshed geo databases.
func (s *Server) registerNode(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	// The signed payload intentionally excludes the IP: a client cannot know
	// its NAT-external address, while the server derives it from the transport
	// and never trusts a client claim.
	req, err := meshauth.ParseSigned(
		r.Header.Get(meshauth.HeaderAddress),
		r.Header.Get(meshauth.HeaderTimestamp),
		r.Header.Get(meshauth.HeaderSignature),
		"register")
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	if s.Geo == nil {
		writeError(w, http.StatusServiceUnavailable, errGeoUnavailable)
		return
	}
	info, err := s.Geo.Lookup(ip)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	node, err := s.Store.UpsertNode(&store.Node{
		MeshAddress: req.Address,
		CountryISO:  info.CountryISO,
		CountryName: info.CountryName,
		ASN:         info.ASN,
		ASNOrg:      info.ASNOrg,
		Tier:        string(info.Tier),
		IP:          ip.String(),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"node":        node,
		"tier_source": "backend-geo-db",
		"note":        "clients cannot self-report country or tier",
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

// listNodes is the public node directory.
func (s *Server) listNodes(w http.ResponseWriter, _ *http.Request) {
	nodes, err := s.Store.ListOnlineNodes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// Never expose raw peer IPs in the public directory.
	public := make([]map[string]any, 0, len(nodes))
	for _, n := range nodes {
		public = append(public, map[string]any{
			"mesh_address":   n.MeshAddress,
			"country_iso":    n.CountryISO,
			"country":        n.CountryName,
			"tier":           n.Tier,
			"asn_org":        n.ASNOrg,
			"is_datacenter":  n.Tier == string(geo.TierDatacenter),
			"last_seen":      n.LastSeen,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": public})
}
