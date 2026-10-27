// Package meshauth authenticates requests signed by a mesh participant's
// Stellar ed25519 key. Identity is the G... address: possession of the
// matching secret key is the only credential needed.
package meshauth

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"
)

// SignatureMaxAge bounds replay exposure for stateless signed requests.
const SignatureMaxAge = 5 * time.Minute

// Header names used by the signed HTTP scheme.
const (
	HeaderAddress   = "X-DMesh-Address"
	HeaderTimestamp = "X-DMesh-Timestamp"
	HeaderSignature = "X-DMesh-Signature"
	HeaderSession   = "X-DMesh-Session"
)

// CanonicalPayload builds the exact byte string a client signs. All parties
// must use this construction verbatim.
func CanonicalPayload(address string, unixTs int64, parts ...string) []byte {
	all := append([]string{"dmesh-v1", address, strconv.FormatInt(unixTs, 10)}, parts...)
	return []byte(strings.Join(all, "\n"))
}

// Verify checks a base64 ed25519 signature against the address's public key.
func Verify(address string, payload []byte, sigBase64 string) error {
	kp, err := keypair.ParseAddress(address)
	if err != nil {
		return fmt.Errorf("meshauth: invalid address %q: %w", address, err)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigBase64))
	if err != nil {
		return fmt.Errorf("meshauth: signature is not base64: %w", err)
	}
	if err := kp.Verify(payload, sig); err != nil {
		return fmt.Errorf("meshauth: signature verification failed: %w", err)
	}
	return nil
}

// SignedRequest is the parsed triple from request headers.
type SignedRequest struct {
	Address   string
	Timestamp int64
	Signature string
}

// ParseSigned validates the header triple, timestamp freshness and signature
// over the supplied payload parts.
func ParseSigned(addr, ts, sig string, parts ...string) (*SignedRequest, error) {
	if addr == "" || ts == "" || sig == "" {
		return nil, errors.New("meshauth: missing signed-request headers")
	}
	unixTs, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("meshauth: invalid timestamp: %w", err)
	}
	age := time.Since(time.Unix(unixTs, 0))
	if age > SignatureMaxAge || age < -SignatureMaxAge {
		return nil, errors.New("meshauth: signature timestamp outside the allowed window")
	}
	payload := CanonicalPayload(addr, unixTs, parts...)
	if err := Verify(addr, payload, sig); err != nil {
		return nil, err
	}
	return &SignedRequest{Address: addr, Timestamp: unixTs, Signature: sig}, nil
}
