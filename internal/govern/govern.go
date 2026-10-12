// Package govern is the enterprise governance layer (v3): virtual API keys,
// authentication, per-key rate limiting and token budgets, MCP tool
// permission filtering, and structured audit logging. All of it composes as
// plain net/http middleware — no framework, no router takeover: Chain wraps
// the existing mux and endpoints stay untouched.
//
// When no keys are configured the auth middleware is simply not installed,
// so the offline demo keeps zero friction.
package govern

import (
	"context"
	"net"
	"net/http"
	"strings"
)

// Middleware is a plain http.Handler decorator.
type Middleware func(http.Handler) http.Handler

// Chain folds middleware around h; the first middleware is outermost.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// --- request identity context ---

type ctxKey int

const (
	identityKey ctxKey = iota
	requestIDKey
)

// Identity is the authenticated caller (zero value = anonymous).
type Identity struct {
	KeyID  string
	Tenant string
	rec    KeyRecord
}

func (i Identity) Anonymous() bool { return i.KeyID == "" }

// Record exposes the underlying key record (rate/budget/tool permissions).
func (i Identity) Record() KeyRecord { return i.rec }

// From extracts identity from ctx (ok=false when anonymous/ungoverned).
func From(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey).(Identity)
	return id, ok && !id.Anonymous()
}

func withIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey, id)
}

// RequestID injects a per-request id (used in audit logs and SSE traces) and
// echoes it back in X-Request-ID.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Request-ID")
			if id == "" {
				id = NewID()
			}
			w.Header().Set("X-Request-ID", id)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
		})
	}
}

// RequestIDFromCtx returns the id injected by RequestID ("" if absent).
func RequestIDFromCtx(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

// isLoopback reports whether the request originates from localhost. The
// X-Forwarded-For header is deliberately ignored: behind a proxy the direct
// peer is the proxy, and trusting client-supplied headers to grant access
// would be an auth bypass.
func isLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// prefixMatch reports whether path starts with any prefix (boundary-aware:
// "/api" must not match "/apiv2-evil").
func prefixMatch(path string, prefixes []string) bool {
	for _, p := range prefixes {
		p = strings.TrimRight(p, "/")
		if p == "" {
			continue
		}
		if path == p || strings.HasPrefix(path, p+"/") || strings.HasPrefix(path, p+"?") {
			return true
		}
	}
	return false
}
