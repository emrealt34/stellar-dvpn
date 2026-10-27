// Package cliclient is the reference mesh client used by the dmesh CLI.
// All requests are signed locally with the participant's Stellar secret key;
// the secret never leaves the process.
package cliclient

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"

	"dmesh/internal/meshauth"
)

// Client talks to a dmesh coordinator.
type Client struct {
	BaseURL string
	KP      *keypair.Full
	HTTP    *http.Client
}

// New constructs a client from a coordinator URL and S... secret seed.
func New(baseURL, secret string) (*Client, error) {
	kp, err := keypair.ParseFull(secret)
	if err != nil {
		return nil, fmt.Errorf("invalid secret seed: %w", err)
	}
	return &Client{
		BaseURL: baseURL,
		KP:      kp,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// Address returns the mesh G address.
func (c *Client) Address() string { return c.KP.Address() }

func (c *Client) signedHeaders(parts ...string) (http.Header, error) {
	ts := time.Now().Unix()
	payload := meshauth.CanonicalPayload(c.KP.Address(), ts, parts...)
	sig, err := c.KP.Sign(payload)
	if err != nil {
		return nil, err
	}
	h := http.Header{}
	h.Set(meshauth.HeaderAddress, c.KP.Address())
	h.Set(meshauth.HeaderTimestamp, strconv.FormatInt(ts, 10))
	h.Set(meshauth.HeaderSignature, base64.StdEncoding.EncodeToString(sig))
	return h, nil
}

func (c *Client) do(method, path string, body any, headers http.Header, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Set(k, v)
		}
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s -> %s: %s", method, path, resp.Status, string(data))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decoding response: %w (body=%s)", err, string(data))
		}
	}
	return nil
}

// Register registers the local mesh address as a node.
func (c *Client) Register() (map[string]any, error) {
	headers, err := c.signedHeaders("register")
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := c.do(http.MethodPost, "/api/v1/nodes/register", nil, headers, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Heartbeat refreshes liveness and cumulative traffic counters.
func (c *Client) Heartbeat(up, down int64) error {
	headers, err := c.signedHeaders("heartbeat")
	if err != nil {
		return err
	}
	return c.do(http.MethodPost, "/api/v1/nodes/heartbeat",
		map[string]int64{"up_bytes": up, "down_bytes": down}, headers, nil)
}

// CoordinateResponse is the coordinator's answer to a session request.
type CoordinateResponse struct {
	Session        map[string]any `json:"session"`
	OpenSessionArgs map[string]any `json:"open_session_args"`
	NextStep       string         `json:"next_step"`
}

// CoordinateSession asks the coordinator for a session nonce and contract args.
func (c *Client) CoordinateSession(nodeAddr, tokenAddr string, ttlSeconds uint64) (*CoordinateResponse, error) {
	headers, err := c.signedHeaders("open", nodeAddr, tokenAddr)
	if err != nil {
		return nil, err
	}
	var out CoordinateResponse
	if err := c.do(http.MethodPost, "/api/v1/sessions",
		map[string]any{"node_address": nodeAddr, "token_address": tokenAddr, "ttl_seconds": ttlSeconds},
		headers, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AttachOnChain reports the client-signed open_session result back.
func (c *Client) AttachOnChain(nonce, contractSessionID, txHash string) error {
	headers, err := c.signedHeaders("attach", nonce, txHash)
	if err != nil {
		return err
	}
	return c.do(http.MethodPost, "/api/v1/sessions/onchain",
		map[string]string{"nonce_hex": nonce, "contract_session_id": contractSessionID, "tx_hash": txHash},
		headers, nil)
}

// SubmitCounters sends a signed traffic counter report.
func (c *Client) SubmitCounters(nonce string, up, down int64) error {
	headers, err := c.signedHeaders("counters", nonce)
	if err != nil {
		return err
	}
	return c.do(http.MethodPost, "/api/v1/sessions/counters",
		map[string]any{"nonce_hex": nonce, "up_bytes": up, "down_bytes": down}, headers, nil)
}

// RecordSettlement mirrors an on-chain lifecycle transition.
func (c *Client) RecordSettlement(nonce, status, txHash string, up, down int64) error {
	headers, err := c.signedHeaders("settlement", nonce, status)
	if err != nil {
		return err
	}
	return c.do(http.MethodPost, "/api/v1/sessions/settlement",
		map[string]any{"nonce_hex": nonce, "status": status, "tx_hash": txHash,
			"up_bytes": up, "down_bytes": down}, headers, nil)
}

// Sessions lists sessions where this address is a party.
func (c *Client) Sessions() (map[string]any, error) {
	headers, err := c.signedHeaders("list")
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := c.do(http.MethodGet, "/api/v1/sessions", nil, headers, &out); err != nil {
		return nil, err
	}
	return out, nil
}
