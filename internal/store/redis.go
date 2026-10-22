package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/LZY-keeper/agentcomm/internal/types"
)

// redisUpdateCAS atomically bumps a task record when the caller's Rev matches.
// KEYS[1]=task key; ARGV: rev, updated_at_rfc3339_nano, ttl_ms, new_json.
// Returns the new rev, -1 if missing, -2 on rev mismatch.
var redisUpdateScript = redis.NewScript(`
local cur = redis.call('GET', KEYS[1])
if not cur then return -1 end
local rec = cjson.decode(cur)
if rec.rev ~= tonumber(ARGV[1]) then return -2 end
rec.rev = rec.rev + 1
rec.updated_at = ARGV[2]
redis.call('SET', KEYS[1], cjson.encode(rec), 'PX', ARGV[3])
return rec.rev
`)

// RedisStores is the durable backend: task records + session history in Redis.
// Unlike the memory store it survives restarts, which is what makes parked
// approvals resumable after a deploy (Hub.Resume replays the checkpoint).
type RedisStores struct {
	rdb    *redis.Client
	ttl    time.Duration
	clock  Clock
	closed chan struct{}
}

// NewRedisStores connects to Redis; call Ping to verify connectivity.
func NewRedisStores(addr, password string, db int, taskTTL time.Duration, clock Clock) *RedisStores {
	if taskTTL <= 0 {
		taskTTL = time.Hour
	}
	if clock == nil {
		clock = RealClock{}
	}
	return &RedisStores{
		rdb: redis.NewClient(&redis.Options{Addr: addr, Password: password, DB: db,
			DialTimeout: 3 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second}),
		ttl: taskTTL, clock: clock, closed: make(chan struct{}),
	}
}

// Ping verifies connectivity at startup (fail fast with a clear error).
func (s *RedisStores) Ping(ctx context.Context) error {
	c, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return s.rdb.Ping(c).Err()
}

// Redis exposes the underlying client for co-located counters (v3.2: the
// router's feedback counts share this pool instead of dialing a second
// connection). The gateway reaches it via interface type assertion.
func (s *RedisStores) Redis() *redis.Client { return s.rdb }

func taskKey(id string) string      { return "agw:task:" + id }
func sessIdxKey(sess string) string { return "agw:task:idx:" + sess }
func sessKey(key string) string     { return "agw:sess:" + key }

// Create stores the record under agw:task:<id> with TTL and indexes it by
// session (ZSET scored by creation time) for List.
func (s *RedisStores) Create(ctx context.Context, t TaskRecord) error {
	now := s.clock.Now()
	t.Rev = 1
	t.CreatedAt, t.UpdatedAt, t.ExpiresAt = now, now, now.Add(s.ttl)
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, taskKey(t.ID), b, s.ttl)
	if t.SessionKey != "" {
		pipe.ZAdd(ctx, sessIdxKey(t.SessionKey), redis.Z{Score: float64(now.UnixMilli()), Member: t.ID})
		pipe.Expire(ctx, sessIdxKey(t.SessionKey), s.ttl)
	}
	_, err = pipe.Exec(ctx)
	return err
}

func (s *RedisStores) Get(ctx context.Context, id string) (TaskRecord, error) {
	b, err := s.rdb.Get(ctx, taskKey(id)).Bytes()
	if err == redis.Nil {
		return TaskRecord{}, ErrNotFound
	}
	if err != nil {
		return TaskRecord{}, err
	}
	var t TaskRecord
	if err := json.Unmarshal(b, &t); err != nil {
		return TaskRecord{}, fmt.Errorf("store: decode task %s: %w", id, err)
	}
	return t, nil
}

// Update runs the rev-CAS Lua script with the caller's full record. Note the
// record JSON round-trips through cjson, so time.Time fields serialize via
// RFC3339 — fine for humans/auditors; ordering uses Rev, never timestamps.
func (s *RedisStores) Update(ctx context.Context, t TaskRecord) error {
	t.UpdatedAt = s.clock.Now()
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	res, err := redisUpdateScript.Run(ctx, s.rdb, []string{taskKey(t.ID)},
		t.Rev, t.UpdatedAt.Format(time.RFC3339Nano), s.ttl.Milliseconds(), string(b)).Int64()
	if err != nil {
		return err
	}
	switch res {
	case -1:
		return ErrNotFound
	case -2:
		return ErrConflict
	}
	t.Rev = res
	if t.SessionKey != "" {
		s.rdb.ZAdd(ctx, sessIdxKey(t.SessionKey), redis.Z{Score: float64(t.CreatedAt.UnixMilli()), Member: t.ID})
		s.rdb.Expire(ctx, sessIdxKey(t.SessionKey), s.ttl)
	}
	return nil
}

// List walks the session index (or scans all tasks when session is empty).
func (s *RedisStores) List(ctx context.Context, session string, limit int) ([]TaskRecord, error) {
	var ids []string
	if session != "" {
		r, err := s.rdb.ZRevRange(ctx, sessIdxKey(session), 0, -1).Result()
		if err != nil {
			return nil, err
		}
		ids = r
	} else {
		iter := s.rdb.Scan(ctx, 0, "agw:task:*", 100).Iterator()
		for iter.Next(ctx) {
			if k := iter.Val(); len(k) > len("agw:task:idx:") || !(len(k) > len("agw:task:idx:") && k[:len("agw:task:idx:")] == "agw:task:idx:") {
				ids = append(ids, k[len("agw:task:"):])
			}
		}
		if err := iter.Err(); err != nil {
			return nil, err
		}
	}
	out := make([]TaskRecord, 0, len(ids))
	for _, id := range ids {
		rec, err := s.Get(ctx, id)
		if err != nil {
			continue // expired between index and get — fine
		}
		out = append(out, rec)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *RedisStores) Delete(ctx context.Context, id string) error {
	rec, err := s.Get(ctx, id)
	if err != nil {
		return nil // idempotent
	}
	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, taskKey(id))
	if rec.SessionKey != "" {
		pipe.ZRem(ctx, sessIdxKey(rec.SessionKey), id)
	}
	_, err = pipe.Exec(ctx)
	return err
}

// Close shuts the Redis client.
func (s *RedisStores) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return s.rdb.Close()
}

// --- SessionStore ---

// Append pushes messages onto agw:sess:<key> and keeps the last 512 entries.
func (s *RedisStores) Append(ctx context.Context, sessionKey string, msgs []types.Message) error {
	if sessionKey == "" || len(msgs) == 0 {
		return nil
	}
	pipe := s.rdb.TxPipeline()
	for _, m := range msgs {
		b, err := json.Marshal(m)
		if err != nil {
			return err
		}
		pipe.RPush(ctx, sessKey(sessionKey), b)
	}
	pipe.LTrim(ctx, sessKey(sessionKey), -512, -1)
	pipe.Expire(ctx, sessKey(sessionKey), s.ttl)
	_, err := pipe.Exec(ctx)
	return err
}

// Load returns the most recent maxTurns*2 entries.
func (s *RedisStores) Load(ctx context.Context, sessionKey string, maxTurns int) ([]types.Message, error) {
	if sessionKey == "" {
		return nil, nil
	}
	n := int64(maxTurns * 2)
	if n <= 0 {
		n = 40
	}
	vals, err := s.rdb.LRange(ctx, sessKey(sessionKey), -n, -1).Result()
	if err != nil {
		return nil, err
	}
	out := make([]types.Message, 0, len(vals))
	for _, v := range vals {
		var m types.Message
		if err := json.Unmarshal([]byte(v), &m); err == nil {
			out = append(out, m)
		}
	}
	return out, nil
}

// PendingTasks returns ids of non-terminal tasks (used by Hub.RecoverPending
// after a restart to re-arm parked approvals). Bounded by limit, oldest first.
func (s *RedisStores) PendingTasks(ctx context.Context, limit int) ([]TaskRecord, error) {
	iter := s.rdb.Scan(ctx, 0, "agw:task:*", 100).Iterator()
	var out []TaskRecord
	for iter.Next(ctx) {
		key := iter.Val()
		if len(key) > len("agw:task:idx:") && key[:len("agw:task:idx:")] == "agw:task:idx:" {
			continue
		}
		rec, err := s.Get(ctx, key[len("agw:task:"):])
		if err != nil || rec.Terminal() {
			continue
		}
		out = append(out, rec)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, iter.Err()
}
