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

// errGeoUnavailable is returned when node registration is attempted but the
// geo databases are not installed. The service never falls back to
// client-supplied location data.
var errGeoUnavailable = errors.New(
	"geo databases are not installed on this coordinator; set GEO_COUNTRY_DB and GEO_ASN_DB from the CI-refreshed GeoLite2 files")

// clientIP extracts the peer IP, honouring a single X-Forwarded-For hop only
// when the request is loopback (i.e. behind our own reverse proxy).
func clientIP(r *http.Request) net.IP {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
					if parsed := net.ParseIP(first); parsed != nil {
						return parsed
					}
				}
			}
		}
		if ip := net.ParseIP(host); ip != nil {
			return ip
		}
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return net.ParseIP(r.RemoteAddr)
	}
	return net.ParseIP(ip)
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
