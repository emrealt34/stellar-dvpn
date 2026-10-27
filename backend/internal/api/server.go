// Package api exposes the dmesh coordinator REST API. The coordinator holds
// no user funds and controls no wallets: every payment is executed by the
// smart contract through allowances the users approve themselves.
package api

import (
	"log"
	"net/http"
	"time"

	"dmesh/internal/agent"
	"dmesh/internal/auth"
	"dmesh/internal/config"
	"dmesh/internal/geo"
	"dmesh/internal/metering"
	"dmesh/internal/soroban"
	"dmesh/internal/store"
)

// Server wires coordinator services together.
type Server struct {
	Cfg      *config.Config
	Store    *store.Store
	Geo      geo.Lookup // nil when geo databases are not installed
	Soroban  *soroban.Client
	Settle   *soroban.Settlement // nil until SETTLEMENT_CONTRACT_ID is set
	Metering *metering.Service
	Agent    *agent.Service
	Sender   auth.Sender
	limiter  *rateLimiter
	devCodes map[string]string
}

// New constructs the HTTP handler.
func New(cfg *config.Config, st *store.Store, geoLookup geo.Lookup,
	sorobanClient *soroban.Client, settle *soroban.Settlement,
	meter *metering.Service, ag *agent.Service, sender auth.Sender) http.Handler {
	s := &Server{
		Cfg:      cfg,
		Store:    st,
		Geo:      geoLookup,
		Soroban:  sorobanClient,
		Settle:   settle,
		Metering: meter,
		Agent:    ag,
		Sender:   sender,
		limiter:  newRateLimiter(3, 10*time.Minute),
		devCodes: map[string]string{},
	}
	go s.maintenanceLoop()

	mux := http.NewServeMux()

	// Public infrastructure
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("GET /api/v1/public/stats", s.publicStats)
	mux.HandleFunc("GET /api/v1/public/pricing", s.publicPricing)
	mux.HandleFunc("GET /api/v1/nodes", s.listNodes)

	// Mesh identity (Stellar key signatures, no passwords)
	mux.HandleFunc("POST /api/v1/nodes/register", s.registerNode)
	mux.HandleFunc("POST /api/v1/nodes/heartbeat", s.nodeHeartbeat)

	// Session coordination (on-chain actions are signed by the parties)
	mux.HandleFunc("POST /api/v1/sessions", s.coordinateSession)
	mux.HandleFunc("POST /api/v1/sessions/onchain", s.attachOnChain)
	mux.HandleFunc("POST /api/v1/sessions/counters", s.submitCounters)
	mux.HandleFunc("POST /api/v1/sessions/settlement", s.recordSettlement)
	mux.HandleFunc("GET /api/v1/sessions", s.listSessions)

	// Agent API (metered fetch / CONNECT)
	mux.HandleFunc("GET /api/v1/agent/fetch", s.agentFetch)
	mux.HandleFunc("CONNECT /api/v1/agent/connect", s.agentConnect)

	// Dashboard (email OTP exists here only)
	mux.HandleFunc("POST /api/v1/dashboard/otp/request", s.requestOTP)
	mux.HandleFunc("POST /api/v1/dashboard/otp/verify", s.verifyOTP)
	mux.Handle("POST /api/v1/dashboard/link", s.dashboardRequired(http.HandlerFunc(s.linkNode)))
	mux.Handle("GET /api/v1/dashboard/nodes", s.dashboardRequired(http.HandlerFunc(s.dashboardNodes)))
	if cfg.DevCodes {
		mux.HandleFunc("GET /api/v1/dashboard/otp/debug", s.debugOTP)
	}

	return mux
}

func (s *Server) maintenanceLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		if n, err := s.Store.MarkStaleNodes(s.Cfg.HeartbeatTimeout); err != nil {
			log.Printf("maintenance: stale nodes: %v", err)
		} else if n > 0 {
			log.Printf("maintenance: %d nodes marked offline", n)
		}
		if n, err := s.Store.ExpireDueSessions(); err != nil {
			log.Printf("maintenance: expire sessions: %v", err)
		} else if n > 0 {
			log.Printf("maintenance: %d sessions expired (no funds move after TTL)", n)
		}
	}
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":              "ok",
		"contract_configured": s.Settle != nil,
		"geo_configured":      s.Geo != nil,
		"network":             s.Cfg.NetworkPassphrase,
	})
}

func (s *Server) publicStats(w http.ResponseWriter, _ *http.Request) {
	nodes, err := s.Store.ListOnlineNodes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	countries := map[string]bool{}
	res, dc := 0, 0
	for _, n := range nodes {
		countries[n.CountryISO] = true
		if n.Tier == string(geo.TierDatacenter) {
			dc++
		} else {
			res++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"online_nodes":  len(nodes),
		"countries":     len(countries),
		"residential":   res,
		"datacenter":    dc,
		"custody_model": "non-custodial",
		"settlement":    "soroban-smart-contract",
	})
}

// publicPricing reports the canonical prices. When the contract is configured
// they are read from chain; otherwise the compiled defaults are returned with
// an explicit source flag.
func (s *Server) publicPricing(w http.ResponseWriter, r *http.Request) {
	if s.Settle == nil || s.Cfg.AdminStellarSecret == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"source": "defaults",
			"tiers": map[string]any{
				soroban.TierResidential: map[string]any{"micro_usd_per_gb": 150_000, "usd_per_gb": 0.15},
				soroban.TierDatacenter:  map[string]any{"micro_usd_per_gb": 30_000, "usd_per_gb": 0.03},
			},
			"protocol_fee_bps": 2000,
		})
		return
	}
	ctx := r.Context()
	adminKP, err := soroban.ParseSecret(s.Cfg.AdminStellarSecret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	res, okR, err := s.Settle.GetTier(ctx, adminKP, soroban.TierResidential)
	dc, okD, err2 := s.Settle.GetTier(ctx, adminKP, soroban.TierDatacenter)
	fee, err3 := s.Settle.FeeBPS(ctx, adminKP)
	if err != nil || err2 != nil || err3 != nil {
		writeError(w, http.StatusBadGateway, firstErr(err, err2, err3))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"source": "contract",
		"tiers": map[string]any{
			soroban.TierResidential: map[string]any{"configured": okR, "micro_usd_per_gb": res, "usd_per_gb": float64(res) / 1e6},
			soroban.TierDatacenter:  map[string]any{"configured": okD, "micro_usd_per_gb": dc, "usd_per_gb": float64(dc) / 1e6},
		},
		"protocol_fee_bps": fee,
	})
}
