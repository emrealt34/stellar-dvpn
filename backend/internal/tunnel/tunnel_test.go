package tunnel

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"

	"dmesh/internal/cliclient"
)

type fakeVerifier struct {
	info *cliclient.SessionInfo
}

func (f fakeVerifier) LookupSession(string) (*cliclient.SessionInfo, error) {
	return f.info, nil
}

type fakeCounters struct {
	mu       sync.Mutex
	up, down int64
	calls    int
}

func (f *fakeCounters) SubmitCounters(_ string, up, down int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.up, f.down, f.calls = up, down, f.calls+1
	return nil
}

// plainDialer lets the node reach a loopback test target; production uses the
// netguard SafeDialer, which has its own tests.
func plainDialer(_ context.Context, network, address string) (net.Conn, error) {
	d := net.Dialer{Timeout: 5 * time.Second}
	return d.Dial(network, address)
}

func TestEndToEndTunnelAndMetering(t *testing.T) {
	clientKP, _ := keypair.Random()
	nodeKP, _ := keypair.Random()
	const nonce = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	verifier := fakeVerifier{info: &cliclient.SessionInfo{
		Nonce:         nonce,
		Status:        "opened",
		ClientAddress: clientKP.Address(),
		NodeAddress:   nodeKP.Address(),
		Tier:          "res",
		ExpiresAt:     time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}}
	counters := &fakeCounters{}

	// TLS target forces the transport to issue an HTTP CONNECT through the
	// local proxy; TLS is verified end-to-end across the tunnel.
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("p2p-payload"))
	}))
	defer target.Close()

	nodeLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	node := &NodeServer{KP: nodeKP, Verifier: verifier, Counters: counters, Dial: plainDialer, Guard: func(string) error { return nil }}
	go func() { _ = node.Serve(nodeLn) }()

	proxy := &LocalProxy{ListenAddr: "127.0.0.1:0", NodeTCP: nodeLn.Addr().String(), KP: clientKP, Nonce: nonce}
	proxyLn, err := proxy.Listen()
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = proxy.Serve(proxyLn) }()

	httpClient := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(&url.URL{Scheme: "http", Host: proxyLn.Addr().String()}),
			TLSClientConfig: target.Client().Transport.(*http.Transport).TLSClientConfig,
		},
		Timeout: 10 * time.Second,
	}

	resp, err := httpClient.Get(target.URL)
	if err != nil {
		t.Fatalf("request through P2P tunnel: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	// Release the pooled CONNECT so the tunnel closes and meters on teardown.
	httpClient.CloseIdleConnections()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		counters.mu.Lock()
		c := counters.calls
		counters.mu.Unlock()
		if c > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	counters.mu.Lock()
	defer counters.mu.Unlock()
	if counters.up <= 0 || counters.down <= 0 {
		t.Fatalf("expected metered bytes, got up=%d down=%d", counters.up, counters.down)
	}
}

func TestRejectsForeignHandshake(t *testing.T) {
	nodeKP, _ := keypair.Random()
	attackerKP, _ := keypair.Random()
	const nonce = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	verifier := fakeVerifier{info: &cliclient.SessionInfo{
		Nonce:         nonce,
		Status:        "opened",
		ClientAddress: "Gsomeoneelse", // attacker is not the session client
		NodeAddress:   nodeKP.Address(),
		ExpiresAt:     time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}}
	nodeLn, _ := net.Listen("tcp", "127.0.0.1:0")
	node := &NodeServer{KP: nodeKP, Verifier: verifier, Dial: plainDialer, Guard: func(string) error { return nil }}
	go func() { _ = node.Serve(nodeLn) }()

	if _, err := Dial(context.Background(), nodeLn.Addr().String(), attackerKP, nonce, "example.com:443"); err == nil {
		t.Fatal("a foreign-client handshake must be rejected")
	}
}
