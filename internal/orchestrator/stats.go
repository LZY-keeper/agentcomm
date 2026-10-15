package orchestrator

import (
	"math"
	"sync"
	"time"

	"agentcomm/internal/types"
)

// AgentStats maintains live per-agent latency/success estimates (exponentially
// weighted) and pushes them into the agents' AgentInfo so the load balancer's
// least_response_time strategy and health decisions run on real measurements
// instead of the static defaults v2 shipped with.
//
// Concurrency: one observation mutates only its own agent's slot under the
// mutex; Snapshot is called at routing time.
type AgentStats struct {
	mu     sync.Mutex
	slots  map[string]*agentSlot
	halfLn float64 // decay factor applied per observation window
	maxMS  float64 // cap for EMA input (runaway timeouts shouldn't poison p50)
}

type agentSlot struct {
	emaMS       float64
	initialized bool
	successes   float64
	total       float64
}

// NewAgentStats builds the tracker. It is cheap; nil disables the feedback.
func NewAgentStats() *AgentStats {
	return &AgentStats{slots: map[string]*agentSlot{}, halfLn: 0.8, maxMS: 30000}
}

// Observe records one call outcome (duration + success) for an agent.
func (s *AgentStats) Observe(agentID string, dur time.Duration, ok bool) {
	if s == nil || agentID == "" {
		return
	}
	ms := float64(dur.Microseconds()) / 1000.0
	if ms > s.maxMS || ms < 0 {
		ms = s.maxMS
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	slot := s.slots[agentID]
	if slot == nil {
		slot = &agentSlot{}
		s.slots[agentID] = slot
	}
	if !slot.initialized {
		slot.emaMS = ms
		slot.initialized = true
	} else {
		slot.emaMS = s.halfLn*slot.emaMS + (1-s.halfLn)*ms
	}
	slot.total++
	if ok {
		slot.successes++
	}
	// bounded window so agents can recover after early failures
	if slot.total > 200 {
		slot.total *= 0.9
		slot.successes *= 0.9
	}
}

// Apply folds current estimates into an AgentInfo (latency p50 proxy = EMA,
// success rate = smoothed ratio with a mild prior so one failure ≠ 0%).
func (s *AgentStats) Apply(info *types.AgentInfo) {
	if s == nil || info == nil || info.ID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	slot := s.slots[info.ID]
	if slot == nil || !slot.initialized {
		return
	}
	info.LatencyP50MS = math.Round(slot.emaMS*100) / 100
	if slot.total > 0 {
		rate := (slot.successes + 5) / (slot.total + 10) // Beta(5,5)-ish prior
		info.SuccessRate = math.Round(rate*1000) / 1000
		info.Healthy = rate >= 0.3 // hard-degrade only on sustained failure
	}
}
