package api

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	errTierInvalid     = errors.New("tier must be \"res\" or \"dc\"")
	errGeoFieldTooLong = errors.New("geo field exceeds its maximum length")
	errPayoutTooLong   = errors.New("payout_address exceeds 120 characters")
	errPayoutBadChars  = errors.New("payout_address may contain only alphanumeric characters")
)

// isSafeAddress performs a conservative charset check on the optional payout
// address. Full per-network checksum validation belongs in the client/UI.
func isSafeAddress(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// clientIP extracts the client IP as seen by the platform reverse proxy.
// Render (and similar hosts) terminate TLS and set X-Forwarded-For to the real
// peer, so the first XFF entry is preferred. This value is not a trust
// boundary: node geo/tier claims are self-derived and Stellar-signed, and the
// stored IP is never exposed in the public directory.
func clientIP(r *http.Request) net.IP {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
			if parsed := net.ParseIP(first); parsed != nil {
				return parsed
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}

// rateLimiter is a tiny per-key sliding-window limiter for OTP endpoints.
type rateLimiter struct {
	mu      sync.Mutex
	maxHits int
	window  time.Duration
	hits    map[string][]time.Time
}

func newRateLimiter(maxHits int, window time.Duration) *rateLimiter {
	return &rateLimiter{maxHits: maxHits, window: window, hits: map[string][]time.Time{}}
}

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-l.window)
	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.maxHits {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
