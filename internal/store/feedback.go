// CountStore implementations for the router's Beta feedback (v3.2): learned
// tool success counts survive a restart. Both backends are advisory best-
// effort — a failed save loses only learning state, never correctness.
package store

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// MemoryCountStore keeps feedback counts in-process. Useful for tests and for
// the default offline deployment (learning survives only within one process).
type MemoryCountStore struct {
	mu     sync.RWMutex
	counts map[string][2]float64
}

func NewMemoryCountStore() *MemoryCountStore {
	return &MemoryCountStore{counts: map[string][2]float64{}}
}

func (m *MemoryCountStore) SaveCounts(counts map[string][2]float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counts = counts
	return nil
}

func (m *MemoryCountStore) LoadCounts() (map[string][2]float64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string][2]float64, len(m.counts))
	for k, v := range m.counts {
		out[k] = v
	}
	return out, nil
}

// RedisCountStore persists counts in one HSET (agw:feedback). No CAS needed —
// the last flush wins and counters are additive, so a lost write costs a few
// observations, not consistency.
type RedisCountStore struct {
	rdb *redis.Client
}

const feedbackKey = "agw:feedback"

// NewRedisCountStore wraps an existing Redis client (the gateway passes the
// store client so deployments share one connection pool).
func NewRedisCountStore(rdb *redis.Client) *RedisCountStore {
	return &RedisCountStore{rdb: rdb}
}

func (r *RedisCountStore) SaveCounts(counts map[string][2]float64) error {
	if len(counts) == 0 {
		return nil
	}
	fields := make([]any, 0, len(counts)*2)
	for id, c := range counts {
		fields = append(fields, id, strconv.FormatFloat(c[0], 'f', -1, 64)+","+strconv.FormatFloat(c[1], 'f', -1, 64))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pipe := r.rdb.TxPipeline()
	pipe.Del(ctx, feedbackKey)
	pipe.HSet(ctx, feedbackKey, fields...)
	_, err := pipe.Exec(ctx)
	return err
}

func (r *RedisCountStore) LoadCounts() (map[string][2]float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	m, err := r.rdb.HGetAll(ctx, feedbackKey).Result()
	if err != nil {
		return nil, err
	}
	out := make(map[string][2]float64, len(m))
	for id, v := range m {
		parts := strings.SplitN(v, ",", 2)
		if len(parts) != 2 {
			continue
		}
		a, err1 := strconv.ParseFloat(parts[0], 64)
		b, err2 := strconv.ParseFloat(parts[1], 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out[id] = [2]float64{a, b}
	}
	return out, nil
}
