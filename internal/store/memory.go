package store

import (
	"context"
	"sort"
	"sync"
	"time"

	"agentcomm/internal/types"
)

// MemoryTaskStore is the default TaskStore: an in-memory map with TTL expiry
// (lazy on read + background janitor) and revision-based optimistic locking.
// State is lost on restart — by design; pair with RedisStores when parked
// approvals must survive restarts.
type MemoryTaskStore struct {
	mu      sync.RWMutex
	tasks   map[string]*TaskRecord
	bySess  map[string]map[string]struct{} // sessionKey -> task ids
	ttl     time.Duration
	sweep   time.Duration
	clock   Clock
	stop    chan struct{}
	stopOne sync.Once
}

// NewMemoryTaskStore builds a store. ttl is the task record lifetime (refreshed
// on every Update); sweep is the janitor interval (<=0 disables the janitor —
// expiry then only happens lazily on Get/List). clock may be nil (RealClock).
func NewMemoryTaskStore(ttl, sweep time.Duration, clock Clock) *MemoryTaskStore {
	if ttl <= 0 {
		ttl = time.Hour
	}
	if sweep <= 0 {
		sweep = 30 * time.Second
	}
	if clock == nil {
		clock = RealClock{}
	}
	m := &MemoryTaskStore{
		tasks: map[string]*TaskRecord{}, bySess: map[string]map[string]struct{}{},
		ttl: ttl, sweep: sweep, clock: clock, stop: make(chan struct{}),
	}
	go m.janitor()
	return m
}

func (m *MemoryTaskStore) janitor() {
	t := time.NewTicker(m.sweep)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.sweepExpired()
		}
	}
}

func (m *MemoryTaskStore) sweepExpired() {
	now := m.clock.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, rec := range m.tasks {
		if now.After(rec.ExpiresAt) {
			m.removeLocked(id)
		}
	}
}

// removeLocked deletes a task and its session index (caller holds mu).
func (m *MemoryTaskStore) removeLocked(id string) {
	rec, ok := m.tasks[id]
	if !ok {
		return
	}
	delete(m.tasks, id)
	if set, ok := m.bySess[rec.SessionKey]; ok {
		delete(set, id)
		if len(set) == 0 {
			delete(m.bySess, rec.SessionKey)
		}
	}
}

// Create stores a new task. The record's Rev/ExpiresAt are managed here.
func (m *MemoryTaskStore) Create(_ context.Context, t TaskRecord) error {
	now := m.clock.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tasks[t.ID]; ok {
		return ErrConflict
	}
	t.Rev = 1
	t.CreatedAt, t.UpdatedAt, t.ExpiresAt = now, now, now.Add(m.ttl)
	rec := t
	m.tasks[t.ID] = &rec
	if t.SessionKey != "" {
		if m.bySess[t.SessionKey] == nil {
			m.bySess[t.SessionKey] = map[string]struct{}{}
		}
		m.bySess[t.SessionKey][t.ID] = struct{}{}
	}
	return nil
}

// Get returns a live (non-expired) record; expired records are deleted.
func (m *MemoryTaskStore) Get(_ context.Context, id string) (TaskRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.tasks[id]
	if !ok {
		return TaskRecord{}, ErrNotFound
	}
	if m.clock.Now().After(rec.ExpiresAt) {
		m.removeLocked(id)
		return TaskRecord{}, ErrNotFound
	}
	return *rec, nil
}

// Update applies a CAS write: caller's Rev must match, else ErrConflict.
func (m *MemoryTaskStore) Update(_ context.Context, t TaskRecord) error {
	now := m.clock.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.tasks[t.ID]
	if !ok {
		return ErrNotFound
	}
	if cur.Rev != t.Rev {
		return ErrConflict
	}
	t.Rev = cur.Rev + 1
	t.CreatedAt = cur.CreatedAt
	t.UpdatedAt = now
	t.ExpiresAt = now.Add(m.ttl)
	if t.SessionKey == "" {
		t.SessionKey = cur.SessionKey
	}
	*cur = t
	return nil
}

// List returns up to limit records, newest first; session filters by index.
func (m *MemoryTaskStore) List(_ context.Context, session string, limit int) ([]TaskRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var ids []string
	if session == "" {
		ids = make([]string, 0, len(m.tasks))
		for id := range m.tasks {
			ids = append(ids, id)
		}
	} else {
		for id := range m.bySess[session] {
			ids = append(ids, id)
		}
	}
	now := m.clock.Now()
	out := make([]TaskRecord, 0, len(ids))
	for _, id := range ids {
		rec, ok := m.tasks[id]
		if !ok || now.After(rec.ExpiresAt) {
			continue
		}
		out = append(out, *rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Delete removes a record unconditionally (idempotent).
func (m *MemoryTaskStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeLocked(id)
	return nil
}

// Close stops the janitor goroutine.
func (m *MemoryTaskStore) Close() error {
	m.stopOne.Do(func() { close(m.stop) })
	return nil
}

// MemorySessionStore keeps per-session rolling windows of messages with TTL.
type MemorySessionStore struct {
	mu    sync.RWMutex
	sess  map[string]*SessionRecord
	ttl   time.Duration
	clock Clock
}

// NewMemorySessionStore builds a session store; ttl<=0 defaults to 24h.
func NewMemorySessionStore(ttl time.Duration, clock Clock) *MemorySessionStore {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	if clock == nil {
		clock = RealClock{}
	}
	return &MemorySessionStore{sess: map[string]*SessionRecord{}, ttl: ttl, clock: clock}
}

// Append appends messages and refreshes TTL. Windowing (maxTurns) is applied
// at Load time so the cap stays configurable per call.
func (s *MemorySessionStore) Append(_ context.Context, sessionKey string, msgs []types.Message) error {
	if sessionKey == "" || len(msgs) == 0 {
		return nil
	}
	now := s.clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.sess[sessionKey]
	if !ok {
		rec = &SessionRecord{SessionKey: sessionKey}
		s.sess[sessionKey] = rec
	}
	rec.Messages = append(rec.Messages, msgs...)
	rec.UpdatedAt = now
	rec.ExpiresAt = now.Add(s.ttl)
	return nil
}

// Load returns the most recent maxTurns user/agent messages (expired → none).
func (s *MemorySessionStore) Load(_ context.Context, sessionKey string, maxTurns int) ([]types.Message, error) {
	if sessionKey == "" {
		return nil, nil
	}
	now := s.clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.sess[sessionKey]
	if !ok || now.After(rec.ExpiresAt) {
		delete(s.sess, sessionKey)
		return nil, nil
	}
	msgs := rec.Messages
	n := maxTurns * 2 // user+agent pair per turn
	if n <= 0 {
		n = 40
	}
	if len(msgs) > n {
		msgs = msgs[len(msgs)-n:]
	}
	out := make([]types.Message, len(msgs))
	copy(out, msgs)
	return out, nil
}

// Close stops nothing (no janitor); satisfies the interface.
func (s *MemorySessionStore) Close() error { return nil }
