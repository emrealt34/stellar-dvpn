// Package agent implements the pay-per-use agent API: authenticated HTTP
// fetching and a CONNECT proxy whose bytes are metered against an open dmesh
// session. No money moves here — counters become evidence that the two parties
// settle through the Soroban contract with transfer_from allowances.
package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"dmesh/internal/meshauth"
	"dmesh/internal/metering"
	"dmesh/internal/store"
)

// Service serves metered agent requests.
type Service struct {
	Store    *store.Store
	Metering *metering.Service
	HTTP     *http.Client
}

// New constructs the agent service.
func New(st *store.Store, meter *metering.Service) *Service {
	return &Service{
		Store:    st,
		Metering: meter,
		HTTP: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil
		}},
	}
}

// authenticate resolves the signed headers to an active session owned by
// the signer. parts are extra payload components (e.g. target URL).
func (s *Service) authenticate(r *http.Request, parts ...string) (*store.Session, error) {
	req, err := meshauth.ParseSigned(
		r.Header.Get(meshauth.HeaderAddress),
		r.Header.Get(meshauth.HeaderTimestamp),
		r.Header.Get(meshauth.HeaderSignature),
		parts...)
	if err != nil {
		return nil, err
	}
	nonce := r.Header.Get(meshauth.HeaderSession)
	if nonce == "" {
		return nil, errors.New("missing " + meshauth.HeaderSession + " header")
	}
	session, err := s.Store.SessionByNonce(nonce)
	if err != nil {
		return nil, fmt.Errorf("unknown session: %w", err)
	}
	if session.ClientAddress != req.Address {
		return nil, errors.New("signer is not the session client")
	}
	if session.Status != "opened" && session.Status != "proposed" {
		return nil, errors.New("session is " + session.Status)
	}
	if time.Now().UTC().After(session.ExpiresAt) {
		return nil, errors.New("session expired")
	}
	return session, nil
}

// FetchHandler performs a server-side GET and meters the response bytes.
func (s *Service) FetchHandler(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("url")
	if _, err := url.ParseRequestURI(target); err != nil || (!strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://")) {
		http.Error(w, `{"error":"valid http(s) url query parameter required"}`, http.StatusBadRequest)
		return
	}
	session, err := s.authenticate(r, http.MethodGet, target)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusUnauthorized)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	req.Header.Set("User-Agent", "dmesh-agent/2.0")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		http.Error(w, `{"error":"upstream failure: `+err.Error()+`"}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Buffer up to maxBufferedFetch so counters are committed before the
	// body is relayed. Larger streams use the metered CONNECT proxy.
	const maxBufferedFetch = 25 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBufferedFetch+1))
	if err != nil {
		http.Error(w, `{"error":"upstream read failure"}`, http.StatusBadGateway)
		return
	}
	if len(body) > maxBufferedFetch {
		http.Error(w, `{"error":"response exceeds 25MB fetch cap, use the CONNECT proxy for large streams"}`,
			http.StatusRequestEntityTooLarge)
		return
	}

	last, _ := s.Store.LastUsageRecord(session.ID, "client")
	var baseUp, baseDown int64
	if last != nil {
		baseUp, baseDown = last.UpBytes, last.DownBytes
	}
	if _, err := s.Metering.Submit(session, session.ClientAddress,
		baseUp+int64(len(target)), baseDown+int64(len(body))); err != nil {
		http.Error(w, `{"error":"metering rejected: `+err.Error()+`"}`, http.StatusConflict)
		return
	}

	w.Header().Set("X-DMesh-Bytes", fmt.Sprintf("%d", len(body)))
	w.Header().Set("X-DMesh-Session", session.NonceHex)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

// ConnectHandler implements an HTTP CONNECT proxy metered by bidirectional bytes.
func (s *Service) ConnectHandler(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Host
	if host == "" {
		host = r.Host
	}
	session, err := s.authenticate(r, http.MethodConnect, host)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusUnauthorized)
		return
	}
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	upstream, err := dialer.DialContext(context.Background(), "tcp", host)
	if err != nil {
		http.Error(w, `{"error":"connect failed: `+err.Error()+`"}`, http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, `{"error":"hijacking unsupported"}`, http.StatusInternalServerError)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer client.Close()
	defer upstream.Close()
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))

	counter := &countingConn{Conn: client}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, counter); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()
	<-done
	<-done

	last, _ := s.Store.LastUsageRecord(session.ID, "client")
	var baseUp, baseDown int64
	if last != nil {
		baseUp, baseDown = last.UpBytes, last.DownBytes
	}
	if _, err := s.Metering.Submit(session, session.ClientAddress,
		baseUp+counter.written, baseDown+counter.read); err != nil {
		fmt.Printf("[dmesh-agent] metering warning session=%s: %v\n", session.NonceHex, err)
	}
}

type countingConn struct {
	net.Conn
	read    int64
	written int64
}

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.read += int64(n)
	return n, err
}

func (c *countingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.written += int64(n)
	return n, err
}
