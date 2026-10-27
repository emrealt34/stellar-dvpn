// Package agent implements the pay-per-use agent API: authenticated HTTP
// fetching and a CONNECT proxy whose bytes are metered against an open dmesh
// session. No money moves here — counters become evidence that the two
// parties settle through the Soroban contract with transfer_from allowances.
//
// The proxy is SSRF-hardened: the coordinator only dials public internet
// addresses (netguard), never loopback, private networks or cloud metadata.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"dmesh/internal/meshauth"
	"dmesh/internal/metering"
	"dmesh/internal/netguard"
	"dmesh/internal/store"
)

// maxConcurrentTunnels bounds simultaneous CONNECT tunnels per coordinator so
// idle or abandoned connections cannot exhaust file descriptors.
const maxConcurrentTunnels = 64

// tunnelIdleTimeout closes a CONNECT tunnel with no transfer for this long.
const tunnelIdleTimeout = 5 * time.Minute

// Service serves metered agent requests.
type Service struct {
	Store        *store.Store
	Metering     *metering.Service
	HTTP         *http.Client
	tunnels      chan struct{}
	allowPrivate bool // test/trusted-private deployments only; see AllowPrivateEgress
	dialer       dialContext
}

// dialContext is the dialer interface both the safe and test dialers satisfy.
type dialContext interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// Option customizes a Service.
type Option func(*Service)

// AllowPrivateEgress disables the public-address guard. Production must not
// use it: it permits dialing loopback and private ranges, which exists for
// integration tests and explicitly trusted private deployments.
func AllowPrivateEgress() Option {
	return func(s *Service) {
		s.allowPrivate = true
		s.dialer = &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
		s.HTTP.Transport = &http.Transport{
			DialContext:           s.dialer.DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second,
			MaxIdleConns:          50,
		}
	}
}

// New constructs the agent service with an SSRF-safe HTTP client that only
// reaches public internet addresses.
func New(st *store.Store, meter *metering.Service, opts ...Option) *Service {
	dialer := netguard.SafeDialer()
	s := &Service{
		Store:    st,
		Metering: meter,
		tunnels:  make(chan struct{}, maxConcurrentTunnels),
		dialer:   dialer,
		HTTP: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				DialContext:           dialer.DialContext,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 20 * time.Second,
				MaxIdleConns:          50,
			},
			CheckRedirect: func(r *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("too many redirects")
				}
				return nil
			},
		},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// urlPort returns the explicit port of an http(s) URL, defaulting to 443 for
// https and 80 for http when none is present.
func urlPort(raw string) (uint16, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return 0, err
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, err
		}
		return uint16(n), nil
	}
	if u.Scheme == "https" {
		return 443, nil
	}
	return 80, nil
}

// jsonError writes a properly encoded JSON error (no string concatenation).
func jsonError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
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
		jsonError(w, http.StatusBadRequest, "valid http(s) url query parameter required")
		return
	}
	// Authenticate before revealing any destination policy, then enforce the
	// SSRF guard; the dialer Control hook re-validates at connect time.
	session, err := s.authenticate(r, http.MethodGet, target)
	if err != nil {
		jsonError(w, http.StatusUnauthorized, err.Error())
		return
	}
	if !s.allowPrivate {
		if err := netguard.CheckHost(target); err != nil {
			jsonError(w, http.StatusForbidden, err.Error())
			return
		}
		if port, err := urlPort(target); err == nil {
			if err := netguard.CheckPort(port); err != nil {
				jsonError(w, http.StatusForbidden, err.Error())
				return
			}
		}
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Header.Set("User-Agent", "dmesh-agent/2.0")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		if errors.Is(err, netguard.ErrBlockedDestination) {
			jsonError(w, http.StatusForbidden, netguard.ErrBlockedDestination.Error())
			return
		}
		jsonError(w, http.StatusBadGateway, "upstream failure: "+err.Error())
		return
	}
	defer resp.Body.Close()

	// Buffer up to maxBufferedFetch so counters are committed before the
	// body is relayed. Larger streams use the metered CONNECT proxy.
	const maxBufferedFetch = 25 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBufferedFetch+1))
	if err != nil {
		jsonError(w, http.StatusBadGateway, "upstream read failure")
		return
	}
	if len(body) > maxBufferedFetch {
		jsonError(w, http.StatusRequestEntityTooLarge,
			"response exceeds 25MB fetch cap, use the CONNECT proxy for large streams")
		return
	}

	last, _ := s.Store.LastUsageRecord(session.ID, "client")
	var baseUp, baseDown int64
	if last != nil {
		baseUp, baseDown = last.UpBytes, last.DownBytes
	}
	if _, err := s.Metering.Submit(session, session.ClientAddress,
		baseUp+int64(len(target)), baseDown+int64(len(body))); err != nil {
		jsonError(w, http.StatusConflict, "metering rejected: "+err.Error())
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
		jsonError(w, http.StatusUnauthorized, err.Error())
		return
	}
	if !s.allowPrivate {
		if err := netguard.CheckHostPort(host); err != nil {
			jsonError(w, http.StatusForbidden, err.Error())
			return
		}
	}

	// Acquire a tunnel slot or fail fast under load.
	select {
	case s.tunnels <- struct{}{}:
		defer func() { <-s.tunnels }()
	default:
		jsonError(w, http.StatusServiceUnavailable, "proxy at capacity, retry shortly")
		return
	}

	upstream, err := s.dialer.DialContext(context.Background(), "tcp", host)
	if err != nil {
		if errors.Is(err, netguard.ErrBlockedDestination) {
			jsonError(w, http.StatusForbidden, netguard.ErrBlockedDestination.Error())
			return
		}
		jsonError(w, http.StatusBadGateway, "connect failed: "+err.Error())
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		jsonError(w, http.StatusInternalServerError, "hijacking unsupported")
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		_ = upstream.Close()
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer client.Close()
	defer upstream.Close()
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))

	counter := &countingConn{idleConn: newIdleConn(client)}
	tunnelUpstream := newIdleConn(upstream)
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(tunnelUpstream, counter); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, tunnelUpstream); done <- struct{}{} }()
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

// idleConn resets an activity deadline on every read/write so a stalled peer
// cannot hold a tunnel (and its slot) open indefinitely.
type idleConn struct {
	net.Conn
}

func newIdleConn(c net.Conn) *idleConn {
	_ = c.SetDeadline(time.Now().Add(tunnelIdleTimeout))
	return &idleConn{c}
}

func (c *idleConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	_ = c.Conn.SetDeadline(time.Now().Add(tunnelIdleTimeout))
	return n, err
}

func (c *idleConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	_ = c.Conn.SetDeadline(time.Now().Add(tunnelIdleTimeout))
	return n, err
}

type countingConn struct {
	*idleConn
	read    int64
	written int64
}

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.idleConn.Read(p)
	c.read += int64(n)
	return n, err
}

func (c *countingConn) Write(p []byte) (int, error) {
	n, err := c.idleConn.Write(p)
	c.written += int64(n)
	return n, err
}
