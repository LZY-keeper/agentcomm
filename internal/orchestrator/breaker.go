package orchestrator

import (
	"sync"
	"time"
)

// Breaker state machine (closed/open/half-open), ported from v1 but with the
// dead `execute()` replaced by a real, used guard. Per-agent instances are
// managed by BreakerManager so one failing agent doesn't trip the others.
type breakerState int

const (
	closed breakerState = iota
	open
	halfOpen
)

type breaker struct {
	mu        sync.Mutex
	state     breakerState
	fails     int
	successes int
	threshold int
	openSecs  int
	openUntil time.Time
}

// Allow reports whether a call may proceed; on a half-open probe it allows one.
func (b *breaker) Allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case open:
		if now.After(b.openUntil) {
			b.state = halfOpen
			b.successes = 0
			return true // probe
		}
		return false
	case halfOpen:
		return true // allow probe; Record governs next transition
	default:
		return true
	}
}

func (b *breaker) Record(success bool, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if success {
		switch b.state {
		case halfOpen:
			b.successes++
			if b.successes >= 2 {
				b.state = closed
				b.fails = 0
			}
		default:
			b.fails = 0
		}
		return
	}
	b.fails++
	switch b.state {
	case halfOpen:
		b.trip(now)
	default:
		if b.fails >= b.threshold {
			b.trip(now)
		}
	}
}

func (b *breaker) trip(now time.Time) {
	b.state = open
	b.openUntil = now.Add(time.Duration(b.openSecs) * time.Second)
}

func (b *breaker) State() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return int(b.state)
}

// BreakerManager keeps a breaker per agent id.
type BreakerManager struct {
	mu       sync.Mutex
	breakers map[string]*breaker
	thresh   int
	openSec  int
}

func NewBreakerManager(threshold, openSecs int) *BreakerManager {
	if threshold <= 0 {
		threshold = 5
	}
	if openSecs <= 0 {
		openSecs = 30
	}
	return &BreakerManager{breakers: map[string]*breaker{}, thresh: threshold, openSec: openSecs}
}

func (m *BreakerManager) get(id string) *breaker {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.breakers[id]
	if !ok {
		b = &breaker{state: closed, threshold: m.thresh, openSecs: m.openSec}
		m.breakers[id] = b
	}
	return b
}

func (m *BreakerManager) Allow(id string) bool           { return m.get(id).Allow(time.Now()) }
func (m *BreakerManager) Record(id string, success bool) { m.get(id).Record(success, time.Now()) }
func (m *BreakerManager) State(id string) int            { return m.get(id).State() }
