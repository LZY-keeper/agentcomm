// Package registry is the A2A agent discovery service: an in-memory registry
// with tag/skill index, heartbeat liveness, and automatic eviction of stale
// agents. Exposed over plain HTTP so any A2A peer can register/discover.
// (Ported from v1's AgentRegistry, with graceful shutdown and a real cleanup
// goroutine.)
package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/LZY-keeper/agentcomm/internal/types"
)

type entry struct {
	info   types.AgentInfo
	expiry time.Time
}

// Registry holds registered agents keyed by id, indexed by tag/skill.
type Registry struct {
	mu     sync.RWMutex
	agents map[string]*entry
	index  map[string]map[string]struct{} // skill -> set(agentID)
	ttl    time.Duration
}

func New(ttl time.Duration) *Registry {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &Registry{agents: map[string]*entry{}, index: map[string]map[string]struct{}{}, ttl: ttl}
}

// Register adds/refreshes an agent and indexes its tags+skills.
func (r *Registry) Register(info types.AgentInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	info.Healthy = true
	r.agents[info.ID] = &entry{info: info, expiry: time.Now().Add(r.ttl)}
	for _, skill := range union(info.Tags, info.Skills) {
		if r.index[skill] == nil {
			r.index[skill] = map[string]struct{}{}
		}
		r.index[skill][info.ID] = struct{}{}
	}
}

// Heartbeat refreshes an agent's liveness.
func (r *Registry) Heartbeat(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.agents[id]
	if !ok {
		return false
	}
	e.expiry = time.Now().Add(r.ttl)
	e.info.Healthy = true
	return true
}

// Discover returns healthy agents matching any of the given skills/tags.
// Empty skills returns all healthy agents.
func (r *Registry) Discover(skills ...string) []types.AgentInfo {
	r.cleanup()
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(skills) == 0 {
		out := make([]types.AgentInfo, 0, len(r.agents))
		for _, e := range r.agents {
			if e.info.Healthy {
				out = append(out, e.info)
			}
		}
		return out
	}
	seen := map[string]struct{}{}
	out := []types.AgentInfo{}
	for _, s := range skills {
		for id := range r.index[s] {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			if e := r.agents[id]; e != nil && e.info.Healthy {
				out = append(out, e.info)
			}
		}
	}
	return out
}

func (r *Registry) cleanup() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for id, e := range r.agents {
		if now.After(e.expiry) {
			delete(r.agents, id)
		}
	}
	// rebuild index from live agents
	r.index = map[string]map[string]struct{}{}
	for _, e := range r.agents {
		for _, s := range union(e.info.Tags, e.info.Skills) {
			if r.index[s] == nil {
				r.index[s] = map[string]struct{}{}
			}
			r.index[s][e.info.ID] = struct{}{}
		}
	}
}

func union(a, b []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range append(append([]string{}, a...), b...) {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// Mount registers HTTP routes for agent discovery.
func (r *Registry) Mount(mux *http.ServeMux) {
	mux.HandleFunc("/agents", func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodPost:
			var info types.AgentInfo
			if err := json.NewDecoder(req.Body).Decode(&info); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			r.Register(info)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": info.ID})
		case http.MethodGet:
			skills := strings.Split(req.URL.Query().Get("skill"), ",")
			if len(skills) == 1 && skills[0] == "" {
				skills = nil
			}
			writeJSON(w, http.StatusOK, r.Discover(skills...))
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/agents/heartbeat/", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(req.URL.Path, "/agents/heartbeat/")
		if !r.Heartbeat(id) {
			http.Error(w, "unknown agent", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
}

// Serve starts the registry HTTP server until ctx is cancelled.
func (r *Registry) Serve(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	r.Mount(mux)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
	}()
	return srv.ListenAndServe()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
