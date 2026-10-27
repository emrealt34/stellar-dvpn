package api_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"

	"dmesh/internal/agent"
	"dmesh/internal/api"
	"dmesh/internal/auth"
	"dmesh/internal/config"
	"dmesh/internal/meshauth"
	"dmesh/internal/metering"
	"dmesh/internal/store"
)

type signer struct{ kp *keypair.Full }

func newSigner(t *testing.T) *signer {
	t.Helper()
	kp, err := keypair.Random()
	if err != nil {
		t.Fatal(err)
	}
	return &signer{kp: kp}
}

func (s *signer) headers(t *testing.T, parts ...string) http.Header {
	t.Helper()
	ts := time.Now().Unix()
	sig, err := s.kp.Sign(meshauth.CanonicalPayload(s.kp.Address(), ts, parts...))
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	h.Set(meshauth.HeaderAddress, s.kp.Address())
	h.Set(meshauth.HeaderTimestamp, strconv.FormatInt(ts, 10))
	h.Set(meshauth.HeaderSignature, base64.StdEncoding.EncodeToString(sig))
	return h
}

func newTestServer(t *testing.T) (string, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "dmesh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := config.Load()
	cfg.DBPath = filepath.Join(t.TempDir(), "unused.db")
	cfg.DevCodes = true
	cfg.HeartbeatTimeout = time.Minute
	cfg.SessionDefaultTTL = time.Hour
	cfg.MaxBandwidthBps = 1_500_000_000

	meter := metering.New(st, cfg.MaxBandwidthBps)
	// Integration tests target loopback httptest servers; production keeps the
	// SSRF guard on by constructing agent.New without options.
	agentSvc := agent.New(st, meter, agent.AllowPrivateEgress())
	handler := api.New(cfg, st, nil, nil, meter, agentSvc, auth.ConsoleSender{})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv.URL, st
}

// nodeProfile is the client-derived, signed geo/tier claim a node registers.
// The coordinator no longer performs any IP geo lookup.
type nodeProfile struct {
	Tier        string
	CountryISO  string
	CountryName string
	ASN         uint
	ASNOrg      string
	Payout      string
}

func defaultProfile() nodeProfile {
	return nodeProfile{Tier: "res", CountryISO: "TR", CountryName: "Turkey", ASN: 9121, ASNOrg: "Turk Telekom"}
}

func (p nodeProfile) body() map[string]any {
	return map[string]any{
		"tier": p.Tier, "country_iso": p.CountryISO, "country": p.CountryName,
		"asn": p.ASN, "asn_org": p.ASNOrg, "payout_address": p.Payout,
	}
}

// headers signs every claimed field over the canonical payload.
func (p nodeProfile) headers(t *testing.T, s *signer) http.Header {
	t.Helper()
	return s.headers(t, "register", p.Tier, p.CountryISO, p.CountryName,
		strconv.FormatUint(uint64(p.ASN), 10), p.ASNOrg, p.Payout)
}

func registerTestNode(t *testing.T, url string, s *signer, p nodeProfile) (int, map[string]any) {
	t.Helper()
	return doJSON(t, http.MethodPost, url+"/api/v1/nodes/register", p.body(), p.headers(t, s))
}

func doJSON(t *testing.T, method, url string, body any, headers http.Header) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, vs := range headers {
		req.Header.Set(k, vs[0])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	if len(data) > 0 {
		_ = json.Unmarshal(data, &out)
	}
	return resp.StatusCode, out
}

func TestNodeRegistrationSignedClaimAndDirectory(t *testing.T) {
	url, _ := newTestServer(t)
	node := newSigner(t)
	profile := defaultProfile()

	status, out := registerTestNode(t, url, node, profile)
	if status != http.StatusOK {
		t.Fatalf("register status=%d body=%v", status, out)
	}
	nodeMap, ok := out["node"].(map[string]any)
	if !ok || nodeMap["tier"] != "res" {
		t.Fatalf("expected the signed residential tier to be stored, got %v", out)
	}
	if nodeMap["country_iso"] != "TR" {
		t.Fatalf("signed country claim should be stored, got %v", nodeMap)
	}
	if out["tier_source"] != "client-signed" {
		t.Fatalf("tier source should be client-signed, got %v", out["tier_source"])
	}

	// Tampering with a claimed field after signing must fail verification.
	tampered := profile
	tampered.Tier = "dc"
	if status, _ = doJSON(t, http.MethodPost, url+"/api/v1/nodes/register",
		tampered.body(), profile.headers(t, node)); status != http.StatusUnauthorized {
		t.Fatalf("altered tier with stale signature should be 401, got %d", status)
	}

	// An invalid tier enum is rejected before signature checks.
	bad := defaultProfile()
	bad.Tier = "vpn"
	if status, _ = doJSON(t, http.MethodPost, url+"/api/v1/nodes/register",
		bad.body(), bad.headers(t, node)); status != http.StatusBadRequest {
		t.Fatalf("invalid tier should be 400, got %d", status)
	}

	// Heartbeat keeps the node online.
	status, _ = doJSON(t, http.MethodPost, url+"/api/v1/nodes/heartbeat",
		map[string]int64{"up_bytes": 1000, "down_bytes": 2000}, node.headers(t, "heartbeat"))
	if status != http.StatusOK {
		t.Fatalf("heartbeat status=%d", status)
	}

	// Public directory lists the node without exposing raw IPs.
	resp, err := http.Get(url + "/api/v1/nodes")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var dir map[string]any
	json.NewDecoder(resp.Body).Decode(&dir)
	nodes := dir["nodes"].([]any)
	if len(nodes) != 1 {
		t.Fatalf("expected 1 public node, got %d", len(nodes))
	}
	if _, leaks := nodes[0].(map[string]any)["ip"]; leaks {
		t.Fatalf("public directory must not leak raw node IPs")
	}
}

func TestSignatureRejection(t *testing.T) {
	url, _ := newTestServer(t)
	node := newSigner(t)
	h := defaultProfile().headers(t, node)
	h.Set(meshauth.HeaderSignature, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	status, _ := doJSON(t, http.MethodPost, url+"/api/v1/nodes/register", defaultProfile().body(), h)
	if status != http.StatusUnauthorized {
		t.Fatalf("forged signature should be 401, got %d", status)
	}
}

func TestSessionCoordinateCountersAndSettlement(t *testing.T) {
	url, _ := newTestServer(t)
	node := newSigner(t)
	client := newSigner(t)
	tokenContract := "CDLM3Z4JTESTTESTTESTTESTTESTTESTTESTTESTTEST"

	if status, _ := registerTestNode(t, url, node, defaultProfile()); status != 200 {
		t.Fatal("node registration failed")
	}

	// Client coordinates a session; the coordinator issues a nonce.
	status, out := doJSON(t, http.MethodPost, url+"/api/v1/sessions",
		map[string]any{"node_address": node.kp.Address(), "token_address": tokenContract, "ttl_seconds": 3600},
		client.headers(t, "open", node.kp.Address(), tokenContract))
	if status != http.StatusCreated {
		t.Fatalf("coordinate status=%d body=%v", status, out)
	}
	args := out["open_session_args"].(map[string]any)
	nonce := args["nonce_hex"].(string)
	if args["tier"] != "res" {
		t.Fatalf("tier should come from node geo record, got %v", args)
	}

	// Unknown node must be rejected.
	other := newSigner(t)
	status, _ = doJSON(t, http.MethodPost, url+"/api/v1/sessions",
		map[string]any{"node_address": other.kp.Address(), "token_address": tokenContract},
		client.headers(t, "open", other.kp.Address(), tokenContract))
	if status != http.StatusNotFound {
		t.Fatalf("unknown node should be 404, got %d", status)
	}

	// The session stays pending and cannot accept traffic until the client
	// attaches the real on-chain open_session identifiers.
	openTxHash := "deadbeef"
	if status, _ = doJSON(t, http.MethodPost, url+"/api/v1/sessions/onchain",
		map[string]any{"nonce_hex": nonce, "contract_session_id": "7", "tx_hash": openTxHash},
		client.headers(t, "attach", nonce, openTxHash)); status != http.StatusNoContent {
		t.Fatalf("attach on-chain open tx should be 204, got %d", status)
	}

	// Both parties submit counters; monotonicity is enforced.
	if status, _ = doJSON(t, http.MethodPost, url+"/api/v1/sessions/counters",
		map[string]any{"nonce_hex": nonce, "up_bytes": 500, "down_bytes": 900},
		client.headers(t, "counters", nonce)); status != http.StatusAccepted {
		t.Fatalf("client counters failed: %d", status)
	}
	if status, _ = doJSON(t, http.MethodPost, url+"/api/v1/sessions/counters",
		map[string]any{"nonce_hex": nonce, "up_bytes": 520, "down_bytes": 950},
		node.headers(t, "counters", nonce)); status != http.StatusAccepted {
		t.Fatalf("node counters failed: %d", status)
	}
	status, _ = doJSON(t, http.MethodPost, url+"/api/v1/sessions/counters",
		map[string]any{"nonce_hex": nonce, "up_bytes": 10, "down_bytes": 20},
		node.headers(t, "counters", nonce))
	if status != http.StatusConflict {
		t.Fatalf("regressing counters should be 409, got %d", status)
	}

	// A stranger cannot submit counters for a session.
	stranger := newSigner(t)
	status, _ = doJSON(t, http.MethodPost, url+"/api/v1/sessions/counters",
		map[string]any{"nonce_hex": nonce, "up_bytes": 600, "down_bytes": 1000},
		stranger.headers(t, "counters", nonce))
	if status != http.StatusConflict {
		t.Fatalf("non-party report should conflict, got %d", status)
	}

	// Settlement mirror: node records the on-chain confirmation.
	status, _ = doJSON(t, http.MethodPost, url+"/api/v1/sessions/settlement",
		map[string]any{"nonce_hex": nonce, "status": "settled", "tx_hash": "abc123", "up_bytes": 520, "down_bytes": 950},
		node.headers(t, "settlement", nonce, "settled"))
	if status != http.StatusNoContent {
		t.Fatalf("settlement mirror failed: %d", status)
	}

	// Settled sessions reject further counter reports.
	status, _ = doJSON(t, http.MethodPost, url+"/api/v1/sessions/counters",
		map[string]any{"nonce_hex": nonce, "up_bytes": 700, "down_bytes": 1100},
		client.headers(t, "counters", nonce))
	if status != http.StatusConflict {
		t.Fatalf("settled session must reject counters, got %d", status)
	}
}

func TestAgentFetchIsMeteredAndAuthenticated(t *testing.T) {
	url, _ := newTestServer(t)
	node := newSigner(t)
	client := newSigner(t)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), 4096))
	}))
	defer target.Close()

	registerTestNode(t, url, node, defaultProfile())
	_, out := doJSON(t, http.MethodPost, url+"/api/v1/sessions",
		map[string]any{"node_address": node.kp.Address(), "token_address": "CTEST"},
		client.headers(t, "open", node.kp.Address(), "CTEST"))
	nonce := out["open_session_args"].(map[string]any)["nonce_hex"].(string)

	// Before the on-chain open_session is attached, the session is pending and
	// the data plane must refuse to serve any traffic (no free-riding).
	preReq, _ := http.NewRequest(http.MethodGet, url+"/api/v1/agent/fetch?url="+target.URL, nil)
	preH := client.headers(t, http.MethodGet, target.URL)
	preReq.Header.Set(meshauth.HeaderAddress, preH.Get(meshauth.HeaderAddress))
	preReq.Header.Set(meshauth.HeaderTimestamp, preH.Get(meshauth.HeaderTimestamp))
	preReq.Header.Set(meshauth.HeaderSignature, preH.Get(meshauth.HeaderSignature))
	preReq.Header.Set(meshauth.HeaderSession, nonce)
	preResp, err := http.DefaultClient.Do(preReq)
	if err != nil {
		t.Fatal(err)
	}
	preResp.Body.Close()
	if preResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("pending session must not reach the data plane, got status=%d", preResp.StatusCode)
	}

	// Attach the on-chain open_session; the session is now opened and servable.
	if status, _ := doJSON(t, http.MethodPost, url+"/api/v1/sessions/onchain",
		map[string]any{"nonce_hex": nonce, "contract_session_id": "9", "tx_hash": "cafe1234"},
		client.headers(t, "attach", nonce, "cafe1234")); status != http.StatusNoContent {
		t.Fatalf("attach open tx should be 204, got %d", status)
	}

	req, _ := http.NewRequest(http.MethodGet, url+"/api/v1/agent/fetch?url="+target.URL, nil)
	h := client.headers(t, http.MethodGet, target.URL)
	req.Header.Set(meshauth.HeaderAddress, h.Get(meshauth.HeaderAddress))
	req.Header.Set(meshauth.HeaderTimestamp, h.Get(meshauth.HeaderTimestamp))
	req.Header.Set(meshauth.HeaderSignature, h.Get(meshauth.HeaderSignature))
	req.Header.Set(meshauth.HeaderSession, nonce)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("agent fetch status=%d body=%s", resp.StatusCode, body)
	}
	if resp.Header.Get("X-DMesh-Bytes") != "4096" {
		t.Fatalf("expected 4096 metered bytes, got header %q", resp.Header.Get("X-DMesh-Bytes"))
	}

	// Missing signature must be rejected.
	resp2, err := http.Get(url + "/api/v1/agent/fetch?url=" + target.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned agent request should be 401, got %d", resp2.StatusCode)
	}
}

func TestDashboardOTPFlow(t *testing.T) {
	url, st := newTestServer(t)
	email := "operator@example.com"

	status, out := doJSON(t, http.MethodPost, url+"/api/v1/dashboard/otp/request",
		map[string]string{"email": email}, nil)
	if status != http.StatusAccepted {
		t.Fatalf("otp request failed: %d %v", status, out)
	}
	resp, err := http.Get(url + "/api/v1/dashboard/otp/debug?email=" + email)
	if err != nil {
		t.Fatal(err)
	}
	var dbg map[string]string
	json.NewDecoder(resp.Body).Decode(&dbg)
	resp.Body.Close()
	code := dbg["code"]
	if code == "" {
		t.Fatal("dev OTP code not exposed in dev mode")
	}

	status, out = doJSON(t, http.MethodPost, url+"/api/v1/dashboard/otp/verify",
		map[string]string{"email": email, "code": code}, nil)
	if status != http.StatusOK {
		t.Fatalf("otp verify failed: %d %v", status, out)
	}
	token := out["token"].(string)

	// Linking requires both a dashboard JWT and a mesh signature.
	node := newSigner(t)
	registerTestNode(t, url, node, defaultProfile())
	req, _ := http.NewRequest(http.MethodPost, url+"/api/v1/dashboard/link",
		bytes.NewReader(mustJSON(map[string]string{"mesh_address": node.kp.Address()})))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	h := node.headers(t, "link", node.kp.Address())
	req.Header.Set(meshauth.HeaderAddress, h.Get(meshauth.HeaderAddress))
	req.Header.Set(meshauth.HeaderTimestamp, h.Get(meshauth.HeaderTimestamp))
	req.Header.Set(meshauth.HeaderSignature, h.Get(meshauth.HeaderSignature))
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNoContent {
		t.Fatalf("link status=%d", resp2.StatusCode)
	}
	nodes, err := st.NodesByEmail(email)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("expected 1 linked node, got %d err=%v", len(nodes), err)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func TestNodeRegistrationPayoutAddress(t *testing.T) {
	url, _ := newTestServer(t)
	node := newSigner(t)
	const payout = "GDO46GQ3M6V6VY2KZQJXH3DGR7AH4G5J3P2T2VY2KZQJXH3DGR7AH"

	// A signed payout address is stored and surfaced in the directory.
	profile := defaultProfile()
	profile.Payout = payout
	status, out := registerTestNode(t, url, node, profile)
	if status != http.StatusOK {
		t.Fatalf("register with payout address status=%d body=%v", status, out)
	}
	if got := out["node"].(map[string]any)["payout_address"]; got != payout {
		t.Fatalf("payout address not stored, got %v", got)
	}

	// Re-registering without the field must preserve the stored address.
	if status, _ = registerTestNode(t, url, node, defaultProfile()); status != http.StatusOK {
		t.Fatalf("plain re-register status=%d", status)
	}
	resp, err := http.Get(url + "/api/v1/nodes")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var dir struct {
		Nodes []map[string]any `json:"nodes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&dir); err != nil {
		t.Fatal(err)
	}
	if len(dir.Nodes) != 1 || dir.Nodes[0]["payout_address"] != payout {
		t.Fatalf("directory should retain payout address, got %+v", dir.Nodes)
	}

	// Disallowed characters are rejected.
	badSigner := newSigner(t)
	bad := defaultProfile()
	bad.Payout = "not-a-valid-address"
	if status, _ = doJSON(t, http.MethodPost, url+"/api/v1/nodes/register",
		bad.body(), bad.headers(t, badSigner)); status != http.StatusBadRequest {
		t.Fatalf("invalid payout charset should be 400, got %d", status)
	}
}

func TestSecurityHeadersAndBodyLimit(t *testing.T) {
	url, _ := newTestServer(t)

	resp, err := http.Get(url + "/api/v1/nodes")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	for _, h := range []string{"X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy", "Content-Security-Policy"} {
		if resp.Header.Get(h) == "" {
			t.Errorf("missing security header %s", h)
		}
	}

	// An oversized JSON body is rejected by the 1 MiB limit rather than buffered.
	big := bytes.Repeat([]byte("a"), (1<<20)+1024)
	req, _ := http.NewRequest(http.MethodPost, url+"/api/v1/dashboard/otp/request", bytes.NewReader(big))
	req.Header.Set("Content-Type", "application/json")
	rec, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Body.Close()
	if rec.StatusCode == http.StatusOK {
		t.Fatalf("oversized body must not be accepted, got %d", rec.StatusCode)
	}
}
