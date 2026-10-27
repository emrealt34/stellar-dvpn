package tunnel_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"

	"dmesh/internal/agent"
	"dmesh/internal/api"
	"dmesh/internal/auth"
	"dmesh/internal/cliclient"
	"dmesh/internal/config"
	"dmesh/internal/metering"
	"dmesh/internal/store"
	"dmesh/internal/tunnel"
)

// TestLivePeerToPeerEgress drives the real coordinator, node tunnel server and
// client CONNECT proxy against a public internet host. It is opt-in because it
// requires outbound network: run with DMESH_RUN_NET_TEST=1.
func TestLivePeerToPeerEgress(t *testing.T) {
	if os.Getenv("DMESH_RUN_NET_TEST") == "" {
		t.Skip("set DMESH_RUN_NET_TEST=1 to run the outbound-network integration test")
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := config.Load()
	cfg.DevCodes = true
	cfg.HeartbeatTimeout = time.Minute
	cfg.SessionDefaultTTL = time.Hour
	cfg.MaxBandwidthBps = 1_500_000_000
	meter := metering.New(st, cfg.MaxBandwidthBps)
	handler := api.New(cfg, st, nil, nil, meter, agent.New(st, meter), auth.ConsoleSender{})
	coord := httptest.NewServer(handler)
	defer coord.Close()

	nodeKP, _ := keypair.Random()
	clientKP, _ := keypair.Random()

	nodeClient, err := cliclient.New(coord.URL, nodeKP.Seed())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nodeClient.Register(cliclient.NodeClaim{
		Tier: "dc", CountryISO: "US", CountryName: "United States",
		ASN: 15169, ASNOrg: "Google LLC",
	}); err != nil {
		t.Fatalf("register node: %v", err)
	}

	clientClient, err := cliclient.New(coord.URL, clientKP.Seed())
	if err != nil {
		t.Fatal(err)
	}
	coordinated, err := clientClient.CoordinateSession(nodeKP.Address(), "CTESTTOKEN", 0)
	if err != nil {
		t.Fatalf("coordinate: %v", err)
	}
	nonce, _ := coordinated.Session["nonce_hex"].(string)
	if len(nonce) != 64 {
		t.Fatalf("bad nonce: %q", nonce)
	}
	// Local test: record a stand-in open tx so the session becomes "opened".
	if err := clientClient.AttachOnChain(nonce, "1", "local-test-tx"); err != nil {
		t.Fatalf("attach: %v", err)
	}

	nodeLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	node := &tunnel.NodeServer{KP: nodeKP, Verifier: nodeClient, Counters: nodeClient}
	go func() { _ = node.Serve(nodeLn) }()

	proxy := &tunnel.LocalProxy{
		ListenAddr: "127.0.0.1:0", NodeTCP: nodeLn.Addr().String(),
		KP: clientKP, Nonce: nonce,
	}
	proxyLn, err := proxy.Listen()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = proxy.Serve(proxyLn) }()

	httpClient := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: proxyLn.Addr().String()})},
		Timeout:   20 * time.Second,
	}

	// Real egress through the P2P tunnel.
	resp, err := httpClient.Get("https://example.com")
	if err != nil {
		t.Fatalf("public request over P2P tunnel: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("example.com status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	httpClient.CloseIdleConnections()

	// The node must have reported cumulative counters to the coordinator.
	session, err := st.SessionByNonce(nonce)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var up, down int64
	for time.Now().Before(deadline) {
		if rec, err := st.LastUsageRecord(session.ID, "node"); err == nil && rec != nil {
			up, down = rec.UpBytes, rec.DownBytes
			if up > 0 && down > 0 {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if up <= 0 || down <= 0 {
		t.Fatalf("node counters not recorded, up=%d down=%d", up, down)
	}

	// Exit policy: an https request to a blocked port forces a CONNECT to
	// example.com:25, which the node's netguard must refuse end to end.
	if _, err := httpClient.Get("https://example.com:25/"); err == nil {
		t.Fatal("CONNECT to blocked port 25 must fail")
	}
}
