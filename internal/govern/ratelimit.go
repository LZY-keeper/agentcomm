package govern

import (
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter is injectable so tests can fake time/budget decisions.
type Limiter interface {
	Allow(key string) (ok bool, retryAfter time.Duration)
}

// TokenBucket is the production Limiter: one x/time/rate.Limiter per key id,
// created lazily with the key's configured (or default) rps/burst.
type TokenBucket struct {
	mu       sync.Mutex
	m        map[string]*rate.Limiter
	defRPS   rate.Limit
	defBurst int
	keys     *Keys // per-key overrides
}

// NewTokenBucketLimiter builds the limiter with gateway-wide defaults; per-key
// RPS/Burst override when the key record sets them.
func NewTokenBucketLimiter(defRPS float64, defBurst int, keys *Keys) *TokenBucket {
	if defRPS <= 0 {
		defRPS = 5
	}
	if defBurst <= 0 {
		defBurst = 10
	}
	return &TokenBucket{m: map[string]*rate.Limiter{}, defRPS: rate.Limit(defRPS), defBurst: defBurst, keys: keys}
}

func (b *TokenBucket) limiterFor(key string, rps float64, burst int) *rate.Limiter {
	b.mu.Lock()
	defer b.mu.Unlock()
	l, ok := b.m[key]
	if !ok {
		if rps <= 0 {
			rps = float64(b.defRPS)
		}
		if burst <= 0 {
			burst = b.defBurst
		}
		l = rate.NewLimiter(rate.Limit(rps), burst)
		b.m[key] = l
	}
	return l
}

// Allow reports whether key may proceed now, with a suggested Retry-After.
func (b *TokenBucket) Allow(key string) (bool, time.Duration) {
	rps, burst := float64(0), 0
	if b.keys != nil {
		// identity lookup already happened in auth; re-derive cheaply from id
		if rec, ok := b.lookupByID(key); ok {
			rps, burst = rec.RPS, rec.Burst
		}
	}
	l := b.limiterFor(key, rps, burst)
	res := l.Reserve()
	if res.Delay() == 0 {
		return true, 0
	}
	d := res.Delay()
	res.Cancel()
	return false, d
}

// lookupByID finds a record by key id (TokenBucket keeps no reverse index;
// per-key overrides are a thin path, O(n) over a small key set).
func (b *TokenBucket) lookupByID(id string) (KeyRecord, bool) {
	if b.keys == nil {
		return KeyRecord{}, false
	}
	for _, rec := range b.keys.byHash {
		if rec.ID == id {
			return rec, true
		}
	}
	return KeyRecord{}, false
}

// RateLimit enforces the limiter per identity; anonymous callers share the
// "_anon" bucket (loopback/dev traffic).
func RateLimit(l Limiter) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, _ := From(r.Context())
			key := "_anon"
			if !id.Anonymous() {
				key = id.KeyID
			}
			ok, retryAfter := l.Allow(key)
			if !ok {
				secs := int(retryAfter/time.Second) + 1
				w.Header().Set("Retry-After", itoa(secs))
				writeGovErr(w, http.StatusTooManyRequests, "rate_limited", "rate limit exceeded; retry later")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
