package api

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/engine"
)

// Principal is a provisioned nonzero account and its funding permission.
type Principal struct {
	User  domain.UserID
	Admin bool
}

// Config sets credentials and per-user admission limits. No default tokens exist.
// Tokens are copied at construction; aliases share a bucket. Nonpositive limits use defaults.
type Config struct {
	Tokens         map[string]Principal
	RatePerSecond  float64
	Burst          int
	RequestTimeout time.Duration
}
type bucket struct {
	tokens float64
	at     time.Time
}
type server struct {
	e       *engine.Engine
	cfg     Config
	mu      sync.Mutex
	buckets map[domain.UserID]*bucket
}
type principalKey struct{}

// NewServer builds the authenticated API. The engine must be running and remains caller-owned.
func NewServer(e *engine.Engine, ready ReadinessFunc, cfg Config) http.Handler {
	if cfg.RatePerSecond <= 0 || math.IsNaN(cfg.RatePerSecond) || math.IsInf(cfg.RatePerSecond, 0) {
		cfg.RatePerSecond = 20
	}
	if cfg.Burst <= 0 {
		cfg.Burst = 40
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 5 * time.Second
	}
	tokens := make(map[string]Principal, len(cfg.Tokens))
	buckets := make(map[domain.UserID]*bucket)
	for token, p := range cfg.Tokens {
		if token != "" && p.User != 0 {
			tokens[token] = p
			buckets[p.User] = &bucket{tokens: float64(cfg.Burst), at: time.Now()}
		}
	}
	cfg.Tokens = tokens
	s := &server{e: e, cfg: cfg, buckets: buckets}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/session", func(w http.ResponseWriter, r *http.Request) {
		p := caller(r)
		writeJSON(w, 200, map[string]any{"user_id": id(uint64(p.User)), "admin": p.Admin})
	})
	mux.HandleFunc("POST /v1/admin/fund", s.fund)
	mux.HandleFunc("POST /v1/orders", s.place)
	mux.HandleFunc("DELETE /v1/orders/{id}", s.cancel)
	mux.HandleFunc("GET /v1/orders/{id}", s.order)
	mux.HandleFunc("GET /v1/requests/{key}", s.lookup)
	mux.HandleFunc("GET /v1/balances", s.balances)
	mux.HandleFunc("GET /v1/book", s.book)
	if ready == nil {
		ready = func() error { return nil }
	}
	health := NewRouter(ready)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path != "/v1" && !strings.HasPrefix(r.URL.Path, "/v1/") {
			health.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Values("Authorization")
		if len(auth) != 1 || !strings.HasPrefix(auth[0], "Bearer ") {
			w.Header().Set("WWW-Authenticate", "Bearer")
			fail(w, 401, "unauthorized")
			return
		}
		p, ok := tokens[strings.TrimPrefix(auth[0], "Bearer ")]
		if !ok {
			w.Header().Set("WWW-Authenticate", "Bearer")
			fail(w, 401, "unauthorized")
			return
		}
		if wait := s.allow(p.User); wait > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(wait))
			fail(w, 429, "rate_limited")
			return
		}
		ctx, done := context.WithTimeout(context.WithValue(r.Context(), principalKey{}, p), cfg.RequestTimeout)
		defer done()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}
func caller(r *http.Request) Principal { return r.Context().Value(principalKey{}).(Principal) }
func (s *server) allow(u domain.UserID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.buckets[u]
	now := time.Now()
	b.tokens = math.Min(float64(s.cfg.Burst), b.tokens+now.Sub(b.at).Seconds()*s.cfg.RatePerSecond)
	b.at = now
	if b.tokens < 1 {
		return int(math.Min(86400, math.Ceil((1-b.tokens)/s.cfg.RatePerSecond)))
	}
	b.tokens--
	return 0
}
