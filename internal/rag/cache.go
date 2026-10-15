package rag

import (
	"container/list"
	"sync"
	"time"
)

// Cache is a thread-safe LRU + TTL cache for embeddings, cutting repeated
// embedding API calls (ported conceptually from v1's textbook LRU store).
type Cache struct {
	max    int
	ttl    time.Duration
	mu     sync.Mutex
	ll     *list.List
	items  map[string]*list.Element
	hits   uint64
	misses uint64
	evicts uint64
}

type cacheEntry struct {
	key string
	vec []float32
	ts  time.Time
}

// NewCache builds a cache. max<=0 means unbounded; ttl<=0 means no expiry.
func NewCache(max int, ttl time.Duration) *Cache {
	return &Cache{max: max, ttl: ttl, ll: list.New(), items: map[string]*list.Element{}}
}

func (c *Cache) Get(key string) ([]float32, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		c.misses++
		return nil, false
	}
	e := el.Value.(*cacheEntry)
	if c.ttl > 0 && time.Since(e.ts) > c.ttl {
		c.remove(el)
		c.misses++
		return nil, false
	}
	c.ll.MoveToFront(el)
	c.hits++
	return e.vec, true
}

func (c *Cache) Put(key string, vec []float32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		e := el.Value.(*cacheEntry)
		e.vec = vec
		e.ts = time.Now()
		c.ll.MoveToFront(el)
		return
	}
	e := &cacheEntry{key: key, vec: vec, ts: time.Now()}
	el := c.ll.PushFront(e)
	c.items[key] = el
	for c.max > 0 && c.ll.Len() > c.max {
		c.remove(c.ll.Back())
		c.evicts++
	}
}

func (c *Cache) remove(el *list.Element) {
	e := c.ll.Remove(el).(*cacheEntry)
	delete(c.items, e.key)
}

// CacheStats reports hit rate telemetry for /metrics and the playground.
type CacheStats struct {
	Hits, Misses, Evictions uint64
	Size, Max               int
	HitRate                 float64
}

func (c *Cache) Stats() CacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := CacheStats{Hits: c.hits, Misses: c.misses, Evictions: c.evicts, Size: c.ll.Len(), Max: c.max}
	total := c.hits + c.misses
	if total > 0 {
		s.HitRate = float64(c.hits) / float64(total)
	}
	return s
}
