package agent

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"

	"dmesh/internal/meshauth"
	"dmesh/internal/metering"
	"dmesh/internal/store"
)

func signedHeaders(t *testing.T, kp *keypair.Full, nonce, method, target string) http.Header {
	t.Helper()
	ts := time.Now().Unix()
	payload := meshauth.CanonicalPayload(kp.Address(), ts, method, target)
	sig, err := kp.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	h.Set(meshauth.HeaderAddress, kp.Address())
	h.Set(meshauth.HeaderTimestamp, strconv.FormatInt(ts, 10))
	h.Set(meshauth.HeaderSignature, base64.StdEncoding.EncodeToString(sig))
	h.Set(meshauth.HeaderSession, nonce)
	return h
}

func openedSession(t *testing.T, st *store.Store, client, node string) string {
	t.Helper()
	nonce := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	sess, err := st.CreatePendingSession(&store.Session{
		ClientAddress: client,
		NodeAddress:   node,
		TokenAddress:  "CTEST",
		Tier:          "res",
		NonceHex:      nonce,
		ExpiresAt:     time.Now().Add(time.Hour).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AttachOpenTx(sess.ID, "1", "txtest"); err != nil {
		t.Fatal(err)
	}
	return nonce
}

// TestFetchSSRFGuard proves the default service refuses a loopback target,
// while the explicitly-enabled test mode can reach it.
func TestFetchSSRFGuard(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	clientKP, _ := keypair.Random()
	nodeKP, _ := keypair.Random()
	nonce := openedSession(t, st, clientKP.Address(), nodeKP.Address())

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))
	defer target.Close()

	call := func(svc *Service) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/fetch?url="+target.URL, nil)
		for k, vs := range signedHeaders(t, clientKP, nonce, http.MethodGet, target.URL) {
			req.Header[k] = vs
		}
		rec := httptest.NewRecorder()
		svc.FetchHandler(rec, req)
		return rec
	}

	// Production configuration: loopback must be blocked.
	safe := New(st, metering.New(st, 1_500_000_000))
	if rec := call(safe); rec.Code != http.StatusForbidden {
		t.Fatalf("safe proxy must block loopback, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Explicit test/private mode: the same request succeeds and is metered.
	private := New(st, metering.New(st, 1_500_000_000), AllowPrivateEgress())
	rec := call(private)
	if rec.Code != http.StatusOK {
		t.Fatalf("private-enabled proxy should succeed, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-DMesh-Bytes") != "5" {
		t.Fatalf("expected 5 metered bytes, got %q", rec.Header().Get("X-DMesh-Bytes"))
	}
}

func TestFetchRejectsUnauthenticated(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "b.db"))
	defer st.Close()
	svc := New(st, metering.New(st, 1_500_000_000))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/fetch?url=https://example.com", nil)
	rec := httptest.NewRecorder()
	svc.FetchHandler(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned request must be 401, got %d", rec.Code)
	}
}
