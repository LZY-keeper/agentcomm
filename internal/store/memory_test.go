package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"agentcomm/internal/types"
)

// fakeClock is a manually-advanced clock for deterministic TTL tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newRec(id, session string) TaskRecord {
	return TaskRecord{
		Task:       types.Task{ID: id, State: types.TaskSubmitted, Context: "ctx-1"},
		SessionKey: session,
	}
}

func TestMemoryTaskStore_CRUDAndCAS(t *testing.T) {
	s := NewMemoryTaskStore(time.Hour, 0, newFakeClock())
	defer s.Close()
	ctx := context.Background()

	if err := s.Create(ctx, newRec("t1", "sess-a")); err != nil {
		t.Fatalf("create: %v", err)
	}
	// duplicate create → conflict
	if err := s.Create(ctx, newRec("t1", "sess-a")); !errors.Is(err, ErrConflict) {
		t.Fatalf("dup create err=%v want ErrConflict", err)
	}
	got, err := s.Get(ctx, "t1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Rev != 1 {
		t.Fatalf("rev=%d want 1", got.Rev)
	}
	// stale CAS → conflict
	stale := got
	stale.Rev = 99
	if err := s.Update(ctx, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update err=%v want ErrConflict", err)
	}
	// good CAS
	got.State = types.TaskWorking
	got.Answer = "partial"
	if err := s.Update(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	got2, _ := s.Get(ctx, "t1")
	if got2.Rev != 2 || got2.State != types.TaskWorking {
		t.Fatalf("after update rev=%d state=%s", got2.Rev, got2.State)
	}
	// missing
	if _, err := s.Get(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing get err=%v", err)
	}
	if err := s.Delete(ctx, "t1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Get(ctx, "t1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete err=%v", err)
	}
}

func TestMemoryTaskStore_TTLExpiry(t *testing.T) {
	fc := newFakeClock()
	s := NewMemoryTaskStore(10*time.Minute, 0, fc) // no janitor; lazy expiry
	defer s.Close()
	ctx := context.Background()

	_ = s.Create(ctx, newRec("t1", ""))
	if _, err := s.Get(ctx, "t1"); err != nil {
		t.Fatalf("pre-expiry get: %v", err)
	}
	fc.Advance(11 * time.Minute)
	if _, err := s.Get(ctx, "t1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("post-expiry err=%v want ErrNotFound", err)
	}
}

func TestMemoryTaskStore_SessionList(t *testing.T) {
	s := NewMemoryTaskStore(time.Hour, 0, newFakeClock())
	defer s.Close()
	ctx := context.Background()
	_ = s.Create(ctx, newRec("a", "s1"))
	_ = s.Create(ctx, newRec("b", "s1"))
	_ = s.Create(ctx, newRec("c", "s2"))

	got, err := s.List(ctx, "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("s1 list=%d want 2", len(got))
	}
	all, _ := s.List(ctx, "", 0)
	if len(all) != 3 {
		t.Fatalf("all list=%d want 3", len(all))
	}
	lim, _ := s.List(ctx, "", 2)
	if len(lim) != 2 {
		t.Fatalf("limit list=%d want 2", len(lim))
	}
}

func TestMemoryTaskStore_ConcurrentAccess(t *testing.T) {
	s := NewMemoryTaskStore(time.Hour, 0, newFakeClock())
	defer s.Close()
	ctx := context.Background()
	_ = s.Create(ctx, newRec("t", ""))

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if rec, err := s.Get(ctx, "t"); err == nil {
					rec.StatusMsg = "w"
					_ = s.Update(ctx, rec) // CAS failures are fine under contention
				}
				_, _ = s.List(ctx, "", 10)
			}
		}()
	}
	wg.Wait()
}

func TestMemorySessionStore_WindowAndTTL(t *testing.T) {
	fc := newFakeClock()
	s := NewMemorySessionStore(time.Hour, fc)
	defer s.Close()
	ctx := context.Background()

	var msgs []types.Message
	for i := 0; i < 30; i++ {
		role := types.RoleUser
		if i%2 == 1 {
			role = types.RoleAgent
		}
		msgs = append(msgs, types.Message{Role: role, Content: string(rune('a' + i))})
	}
	_ = s.Append(ctx, "sess", msgs)

	got, _ := s.Load(ctx, "sess", 5)
	if len(got) != 10 { // 5 turns * 2
		t.Fatalf("loaded=%d want 10", len(got))
	}
	if got[0].Content != string(rune('a'+20)) {
		t.Fatalf("window start=%q want %q", got[0].Content, string(rune('a'+20)))
	}
	fc.Advance(2 * time.Hour)
	got, _ = s.Load(ctx, "sess", 5)
	if len(got) != 0 {
		t.Fatalf("expired load=%d want 0", len(got))
	}
}
