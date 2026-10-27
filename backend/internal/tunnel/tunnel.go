// Package tunnel implements the peer-to-peer data plane: a node exposes a
// tunnel listener and a client reaches the internet through it without the
// coordinator carrying any traffic. The coordinator is used only to
// authenticate the session (signaling) and to collect metered counters.
//
// Every tunnel is gated by an on-chain session: the node verifies the client's
// Stellar-signed handshake against the coordinator, applies the same SSRF and
// exit-policy rules as the hosted agent proxy (internal/netguard), and reports
// cumulative byte counters back to the coordinator when the tunnel closes.
package tunnel

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"

	"dmesh/internal/cliclient"
	"dmesh/internal/meshauth"
	"dmesh/internal/netguard"
)

const (
	protocolVersion = 1
	maxHandshake    = 8 << 10 // 8 KiB for the single JSON handshake line
	maxTunnels      = 128
	idleTimeout     = 5 * time.Minute
	dialTimeout     = 15 * time.Second
)

// SessionVerifier confirms a nonce maps to an active session the caller
// participates in. *cliclient.Client satisfies it.
type SessionVerifier interface {
	LookupSession(nonce string) (*cliclient.SessionInfo, error)
}

// CounterSubmitter records cumulative traffic for a session. *cliclient.Client
// satisfies it.
type CounterSubmitter interface {
	SubmitCounters(nonce string, up, down int64) error
}

// Handshake is the single newline-terminated JSON frame the client sends
// immediately after connecting, before any tunneled bytes.
type Handshake struct {
	Version   int    `json:"v"`
	Nonce     string `json:"nonce"`
	Address   string `json:"address"`
	Timestamp int64  `json:"timestamp"`
	Target    string `json:"target"`
	Signature string `json:"signature"`
}

// Reply is the node's single JSON answer to the handshake.
type Reply struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	UpBytes   int64  `json:"up_bytes,omitempty"`
	DownBytes int64  `json:"down_bytes,omitempty"`
}

// dialFunc allows tests to replace the SSRF-safe dialer.
type dialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// NodeServer accepts authenticated P2P tunnels and relays them to public
// internet targets while metering every byte.
type NodeServer struct {
	KP       *keypair.Full
	Verifier SessionVerifier
	Counters CounterSubmitter
	Dial     dialFunc
	// Guard enforces the SSRF/exit-policy rules; defaults to netguard. Tests
	// may override it to reach loopback targets.
	Guard func(target string) error

	mu     sync.Mutex
	totals map[string]*counters
	sem    chan struct{}
}

type counters struct {
	up, down int64
}

// Serve runs the accept loop until the listener closes.
func (s *NodeServer) Serve(ln net.Listener) error {
	if s.Dial == nil {
		d := netguard.SafeDialer()
		s.Dial = d.DialContext
	}
	if s.Guard == nil {
		s.Guard = netguard.CheckHostPort
	}
	if s.totals == nil {
		s.totals = map[string]*counters{}
	}
	s.sem = make(chan struct{}, maxTunnels)

	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handle(conn)
	}
}

func (s *NodeServer) writeReply(conn net.Conn, r Reply) {
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_ = json.NewEncoder(conn).Encode(r)
}

func (s *NodeServer) handle(client net.Conn) {
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(idleTimeout))

	br := bufio.NewReaderSize(client, maxHandshake)
	line, err := br.ReadString('\n')
	if err != nil {
		return
	}
	var hs Handshake
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &hs); err != nil {
		s.writeReply(client, Reply{Error: "invalid handshake"})
		return
	}
	if hs.Version != protocolVersion {
		s.writeReply(client, Reply{Error: "unsupported protocol version"})
		return
	}
	// Signature binds the nonce and the exact target.
	if _, err := meshauth.ParseSigned(hs.Address, strconv.FormatInt(hs.Timestamp, 10),
		hs.Signature, "tunnel", hs.Nonce, hs.Target); err != nil {
		s.writeReply(client, Reply{Error: "authentication failed: " + err.Error()})
		return
	}

	info, err := s.Verifier.LookupSession(hs.Nonce)
	if err != nil {
		s.writeReply(client, Reply{Error: "unknown or inactive session"})
		return
	}
	if info.ClientAddress != hs.Address || info.NodeAddress != s.KP.Address() {
		s.writeReply(client, Reply{Error: "handshake does not match session parties"})
		return
	}
	if info.Status != "opened" && info.Status != "proposed" {
		s.writeReply(client, Reply{Error: "session is " + info.Status})
		return
	}
	if exp, err := time.Parse(time.RFC3339, info.ExpiresAt); err == nil && time.Now().UTC().After(exp) {
		s.writeReply(client, Reply{Error: "session expired"})
		return
	}
	// Node-side exit policy and SSRF guard on the actual destination.
	if err := s.Guard(hs.Target); err != nil {
		s.writeReply(client, Reply{Error: err.Error()})
		return
	}

	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	default:
		s.writeReply(client, Reply{Error: "node at capacity"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	target, err := s.Dial(ctx, "tcp", hs.Target)
	cancel()
	if err != nil {
		s.writeReply(client, Reply{Error: "dial failed: " + err.Error()})
		return
	}
	defer target.Close()

	if err := json.NewEncoder(client).Encode(Reply{OK: true}); err != nil {
		return
	}

	// Reset to activity-based deadlines for the tunnel lifetime.
	client = &idleConn{Conn: client, timeout: idleTimeout}
	target = &idleConn{Conn: target, timeout: idleTimeout}

	cc := &countingConn{Conn: client}
	tc := &countingConn{Conn: target}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(tc, cc) }() // client -> target (upload)
	go func() { defer wg.Done(); _, _ = io.Copy(cc, tc) }() // target -> client (download)
	wg.Wait()

	up, down := s.accumulate(hs.Nonce, cc.written+cc.read, tc.written+tc.read)
	// Best-effort monotonic counter submission; the tunnel itself is unaffected.
	if s.Counters != nil {
		if err := s.Counters.SubmitCounters(hs.Nonce, up, down); err != nil {
			fmt.Printf("[dmesh-tunnel] counter warning nonce=%s: %v\n", hs.Nonce, err)
		}
	}
}

// accumulate folds this tunnel's bytes into the per-nonce cumulative total.
// Upload is what the node wrote toward the target; download is what the target
// wrote toward the client.
func (s *NodeServer) accumulate(nonce string, clientSent, targetSent int64) (int64, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.totals[nonce]
	if !ok {
		c = &counters{}
		s.totals[nonce] = c
	}
	c.up += clientSent
	c.down += targetSent
	return c.up, c.down
}

// countingConn tracks bytes transferred in each direction.
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

// idleConn refreshes an activity deadline on every read or write.
type idleConn struct {
	net.Conn
	timeout time.Duration
}

func (c *idleConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	_ = c.Conn.SetDeadline(time.Now().Add(c.timeout))
	return n, err
}

func (c *idleConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	_ = c.Conn.SetDeadline(time.Now().Add(c.timeout))
	return n, err
}

// Dial opens one authenticated tunnel to a node and returns a connection that
// transparently reads through the handshake's buffered bytes.
func Dial(ctx context.Context, nodeTCP string, kp *keypair.Full, nonce, target string) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", nodeTCP)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(dialTimeout))

	ts := time.Now().Unix()
	sig, err := kp.Sign(meshauth.CanonicalPayload(kp.Address(), ts, "tunnel", nonce, target))
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	hs := Handshake{
		Version:   protocolVersion,
		Nonce:     nonce,
		Address:   kp.Address(),
		Timestamp: ts,
		Target:    target,
		Signature: base64.StdEncoding.EncodeToString(sig),
	}
	if err := json.NewEncoder(conn).Encode(hs); err != nil {
		_ = conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	var reply Reply
	if err := json.NewDecoder(io.LimitReader(br, maxHandshake)).Decode(&reply); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("tunnel handshake read failed: %w", err)
	}
	if !reply.OK {
		_ = conn.Close()
		return nil, errors.New(reply.Error)
	}
	_ = conn.SetDeadline(time.Time{})
	return &bufferedConn{Conn: conn, r: br}, nil
}

// bufferedConn preserves any bytes already buffered during the handshake.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// LocalProxy exposes an HTTP CONNECT proxy on the client machine and forwards
// every accepted tunnel through a node over the authenticated P2P protocol.
type LocalProxy struct {
	ListenAddr string
	NodeTCP    string
	KP         *keypair.Full
	Nonce      string
}

// Listen creates the local CONNECT listener.
func (p *LocalProxy) Listen() (net.Listener, error) {
	return net.Listen("tcp", p.ListenAddr)
}

// Serve runs the local CONNECT listener until it is closed.
func (p *LocalProxy) Serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go p.handle(conn)
	}
}

func (p *LocalProxy) handle(local net.Conn) {
	defer local.Close()
	br := bufio.NewReader(local)
	req, err := http.ReadRequest(br)
	if err != nil || req.Method != http.MethodConnect {
		_, _ = io.WriteString(local, "HTTP/1.1 405 Method Not Allowed\r\n\r\n")
		return
	}
	target := req.Host
	if !strings.Contains(target, ":") {
		target = net.JoinHostPort(target, "443")
	}
	tunnel, err := Dial(context.Background(), p.NodeTCP, p.KP, p.Nonce, target)
	if err != nil {
		_, _ = io.WriteString(local, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	defer tunnel.Close()
	if _, err := io.WriteString(local, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(tunnel, br); done <- struct{}{} }()
	go func() { _, _ = io.Copy(local, tunnel); done <- struct{}{} }()
	<-done
}
