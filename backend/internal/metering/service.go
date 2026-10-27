// Package metering collects traffic-counter evidence for open sessions.
// It never moves money: the Soroban settlement contract is the sole arbiter
// of payment. This service enforces monotonic counters and an anti-fraud rate
// ceiling so obviously fabricated reports are rejected before they reach the
// two-party on-chain settlement.
package metering

import (
	"errors"
	"fmt"
	"time"

	"dmesh/internal/store"
)

var (
	// ErrSessionNotActive means the session cannot accept counters.
	ErrSessionNotActive = errors.New("session is not active")
	// ErrCountersNotMonotonic means a report regressed vs the previous one.
	ErrCountersNotMonotonic = errors.New("counters must be monotonic")
	// ErrRateCeiling means the reported volume is physically implausible.
	ErrRateCeiling = errors.New("reported rate exceeds the anti-fraud ceiling")
)

// Service validates and persists usage evidence.
type Service struct {
	Store  *store.Store
	MaxBps int64
}

// New constructs a metering service.
func New(st *store.Store, maxBps int64) *Service {
	return &Service{Store: st, MaxBps: maxBps}
}

// Submit validates one counter report from a party and stores it as evidence.
// Reporter must be the client or node address of the session; callers are
// expected to have authenticated the Stellar signature already.
func (s *Service) Submit(session *store.Session, reporter string, up, down int64) (*store.UsageRecord, error) {
	if up < 0 || down < 0 {
		return nil, errors.New("counters cannot be negative")
	}
	if session.Status != "opened" && session.Status != "proposed" {
		return nil, ErrSessionNotActive
	}
	if reporter != session.ClientAddress && reporter != session.NodeAddress {
		return nil, fmt.Errorf("reporter %s is not a party to the session", reporter)
	}
	role := "client"
	if reporter == session.NodeAddress {
		role = "node"
	}

	last, err := s.Store.LastUsageRecord(session.ID, role)
	if err != nil {
		return nil, err
	}
	if last != nil && (up < last.UpBytes || down < last.DownBytes) {
		return nil, ErrCountersNotMonotonic
	}

	elapsed := int64(time.Since(session.CreatedAt).Seconds())
	if elapsed < 1 {
		elapsed = 1
	}
	if s.MaxBps > 0 && (up+down) > s.MaxBps*elapsed {
		return nil, ErrRateCeiling
	}

	if err := s.Store.InsertUsageRecord(session.ID, role, up, down); err != nil {
		return nil, err
	}
	return s.Store.LastUsageRecord(session.ID, role)
}
