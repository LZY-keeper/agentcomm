package orchestrator

import (
	"hash/fnv"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LZY-keeper/agentcomm/internal/types"
)

// Strategy enumerates the load-balancing strategies (ported from v1's 6
// strategies, with the consistent-hash bug fixed: selection is keyed).
type Strategy string

const (
	RoundRobin         Strategy = "round_robin"
	Random             Strategy = "random"
	LeastConnections   Strategy = "least_connections"
	WeightedRoundRobin Strategy = "weighted_rr"
	ConsistentHash     Strategy = "consistent_hash"
	LeastResponseTime  Strategy = "least_response_time"
)

// LoadBalancer picks one agent from a candidate set per the configured strategy.
type LoadBalancer struct {
	strategy Strategy
	rr       uint64 // round-robin counter
	conn     map[string]*int64
	wrr      map[string]int // smooth weighted round-robin current weight
	mu       sync.Mutex
	rng      *rand.Rand
}

func NewLoadBalancer(s Strategy) *LoadBalancer {
	if s == "" {
		s = LeastResponseTime
	}
	return &LoadBalancer{strategy: s, conn: map[string]*int64{}, wrr: map[string]int{}, rng: rand.New(rand.NewSource(time.Now().UnixNano()))}
}

// Select chooses an agent. key is used for consistent-hash (e.g. session id).
func (lb *LoadBalancer) Select(agents []types.AgentInfo, key string) types.AgentInfo {
	if len(agents) == 0 {
		return types.AgentInfo{}
	}
	if len(agents) == 1 {
		return agents[0]
	}
	healthy := filterHealthy(agents)
	if len(healthy) == 0 {
		healthy = agents // degrade: consider all if none marked healthy
	}
	switch lb.strategy {
	case RoundRobin:
		i := atomic.AddUint64(&lb.rr, 1)
		return healthy[int(i-1)%len(healthy)]
	case Random:
		lb.mu.Lock()
		i := lb.rng.Intn(len(healthy))
		lb.mu.Unlock()
		return healthy[i]
	case LeastConnections:
		return lb.leastConn(healthy)
	case WeightedRoundRobin:
		return lb.weightedRR(healthy)
	case ConsistentHash:
		return lb.consistentHash(healthy, key)
	default: // LeastResponseTime
		sort.SliceStable(healthy, func(i, j int) bool {
			if healthy[i].LatencyP50MS != healthy[j].LatencyP50MS {
				return healthy[i].LatencyP50MS < healthy[j].LatencyP50MS
			}
			return healthy[i].SuccessRate >= healthy[j].SuccessRate
		})
		return healthy[0]
	}
}

// filterHealthy returns a fresh slice (not aliased to a's backing array) so the
// caller's slice is never mutated by downstream in-place sorts.
func filterHealthy(a []types.AgentInfo) []types.AgentInfo {
	out := make([]types.AgentInfo, 0, len(a))
	for _, x := range a {
		if x.Healthy {
			out = append(out, x)
		}
	}
	return out
}

func (lb *LoadBalancer) leastConn(a []types.AgentInfo) types.AgentInfo {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	var best types.AgentInfo
	bestN := int64(1 << 62)
	for _, x := range a {
		c := lb.conn[x.ID]
		if c == nil {
			var z int64
			c = &z
			lb.conn[x.ID] = c
		}
		if *c < bestN {
			bestN = *c
			best = x
		}
	}
	return best
}

// ConnStart/ConnDone let the orchestrator track in-flight calls for
// least-connections balancing.
func (lb *LoadBalancer) ConnStart(id string) {
	lb.mu.Lock()
	c := lb.conn[id]
	if c == nil {
		var z int64
		c = &z
		lb.conn[id] = c
	}
	lb.mu.Unlock()
	atomic.AddInt64(c, 1)
}
func (lb *LoadBalancer) ConnDone(id string) {
	lb.mu.Lock()
	c := lb.conn[id]
	lb.mu.Unlock()
	if c != nil {
		atomic.AddInt64(c, -1)
	}
}

// weightedRR implements nginx-style smooth weighted round-robin.
func (lb *LoadBalancer) weightedRR(a []types.AgentInfo) types.AgentInfo {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	total := 0
	var best types.AgentInfo
	bestCW := -(1 << 30)
	for _, x := range a {
		w := weightFor(x)
		cw := lb.wrr[x.ID] + w
		lb.wrr[x.ID] = cw
		total += w
		if cw > bestCW {
			bestCW = cw
			best = x
		}
	}
	if best.ID != "" {
		lb.wrr[best.ID] -= total
	}
	return best
}

func weightFor(a types.AgentInfo) int {
	w := int(a.SuccessRate * 10)
	if w < 1 {
		w = 1
	}
	return w
}

// consistentHash maps key onto the sorted agent ring via FNV; same key → same
// agent, with minimal remapping when the set changes (fixes v1's random-key bug).
func (lb *LoadBalancer) consistentHash(a []types.AgentInfo, key string) types.AgentInfo {
	if key == "" {
		key = "default"
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	kh := h.Sum64()
	sort.Slice(a, func(i, j int) bool { return ringPos(a[i].ID) < ringPos(a[j].ID) })
	for _, x := range a {
		if ringPos(x.ID) >= kh {
			return x
		}
	}
	return a[0]
}

func ringPos(id string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	return h.Sum64()
}
