package govern

import (
	"net/http"
	"strings"

	"github.com/LZY-keeper/agentcomm/internal/orchestrator"
)

// AuthOption configures the auth middleware.
type AuthOption func(*authMW)

type authMW struct {
	keys      *Keys
	loopback  bool
	exempt    []string
	protected []string
}

// WithLoopbackAllowed allows unauthenticated localhost callers (default on:
// the offline demo and the Playground must work with zero setup; turn off in
// production deployments behind a proxy).
func WithLoopbackAllowed(b bool) AuthOption { return func(a *authMW) { a.loopback = b } }

// WithExemptPrefixes lists always-public prefixes (health, metrics).
func WithExemptPrefixes(p ...string) AuthOption { return func(a *authMW) { a.exempt = p } }

// WithProtectedPrefixes lists prefixes that require a valid key.
func WithProtectedPrefixes(p ...string) AuthOption { return func(a *authMW) { a.protected = p } }

// Auth enforces virtual-key authentication on protected prefixes.
// Extraction order: Authorization: Bearer <key>, then X-API-Key.
func Auth(keys *Keys, opts ...AuthOption) Middleware {
	a := &authMW{keys: keys, loopback: true,
		exempt:    []string{"/healthz", "/metrics", "/api/health"},
		protected: []string{"/api", "/a2a", "/mcp"}}
	for _, o := range opts {
		o(a)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !prefixMatch(r.URL.Path, a.protected) || prefixMatch(r.URL.Path, a.exempt) ||
				(a.loopback && isLoopback(r)) {
				next.ServeHTTP(w, r)
				return
			}
			raw := extractKey(r)
			if raw == "" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="agentcomm"`)
				writeGovErr(w, http.StatusUnauthorized, "missing_api_key", "missing API key")
				return
			}
			rec, ok := a.keys.Lookup(raw)
			if !ok {
				writeGovErr(w, http.StatusUnauthorized, "invalid_api_key", "invalid API key")
				return
			}
			id := Identity{KeyID: rec.ID, Tenant: rec.Tenant, rec: rec}
			ctx := withIdentity(r.Context(), id)
			ctx = orchestrator.WithPrincipal(ctx, rec.ID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func extractKey(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if rest, ok := strings.CutPrefix(auth, "Bearer "); ok {
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}

func writeGovErr(w http.ResponseWriter, code int, errName, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(`{"error":"` + errName + `","message":"` + msg + `"}`))
}
