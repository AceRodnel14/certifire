// Package server holds the app's HTTP handlers.
package server

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"certifire/internal/certgen"
	"certifire/internal/store"
	"certifire/internal/webhook"
)

const (
	sessionCookie = "certifire_session"
	sessionTTL    = 12 * time.Hour
	testInterval  = 10 * time.Second
)

type generator interface {
	Generate(ctx context.Context, spec certgen.Spec) (*certgen.Result, error)
}

// Server is the HTTP handler. The database may attach after start: until then
// /healthz works, /readyz is 503 and database-backed routes answer 503.
type Server struct {
	cfg    Config
	store  atomic.Pointer[store.Store]
	gen    generator
	sender *webhook.Sender

	limiter *limiter // failed password / master-key attempts
	kdf     sem      // concurrent Argon2 work (memory)

	backupAll        sem      // at most one backup-all run at a time
	backupIPLimiter  *limiter // backup-all master-key attempts per peer IP
	backupAllLimiter *limiter // ... and across all peers

	testMu   sync.Mutex
	lastTest map[int64]time.Time

	handler http.Handler
}

// New returns the app's handler; static is the web UI (nil for API only).
func New(cfg Config, static fs.FS) *Server {
	s := &Server{
		cfg:     cfg,
		gen:     certgen.NewGenerator(),
		sender:  webhook.NewSender(),
		limiter: newLimiter(maxFails, failWindow),
		kdf:     make(sem, 2),

		backupAll:        make(sem, 1),
		backupIPLimiter:  newLimiter(backupMaxFails, backupWindow),
		backupAllLimiter: newLimiter(backupMaxFails, backupWindow),
		lastTest:         map[int64]time.Time{},
	}
	s.handler = securityHeaders(sameOrigin(cfg.BaseURL, limitBody(s.routes(static))))
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// SetStore attaches the database once it is ready.
func (s *Server) SetStore(st *store.Store) { s.store.Store(st) }

// Sender is the shared webhook sender (also used by the reminder job).
func (s *Server) Sender() *webhook.Sender { return s.sender }

// ReapSessions deletes expired sessions hourly until ctx ends.
func (s *Server) ReapSessions(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.limiter.sweep()
			s.backupIPLimiter.sweep()
			s.backupAllLimiter.sweep()
			if st := s.store.Load(); st != nil {
				if err := st.ReapSessions(ctx); err != nil && ctx.Err() == nil {
					slog.Error("reap sessions", "err", err)
				}
			}
		}
	}
}

func (s *Server) routes(static fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /readyz", s.handleReady)
	mux.HandleFunc("GET /api/info", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"base_url": s.cfg.BaseURL})
	})

	mux.HandleFunc("GET /api/state", s.open(s.handleState))
	mux.HandleFunc("POST /api/setup", s.open(s.handleSetup))
	mux.HandleFunc("POST /api/register", s.open(s.handleRegister))
	mux.HandleFunc("POST /api/login", s.open(s.handleLogin))
	mux.HandleFunc("POST /api/logout", s.open(s.handleLogout))

	mux.HandleFunc("POST /api/account/password", s.user(s.handleChangePassword))
	mux.HandleFunc("PUT /api/account/theme", s.user(s.handleTheme))
	mux.HandleFunc("GET /api/account/webhooks", s.user(s.handleWebhooksGet))
	mux.HandleFunc("PUT /api/account/webhooks/{kind}", s.user(s.handleWebhookPut))
	mux.HandleFunc("DELETE /api/account/webhooks/{kind}", s.user(s.handleWebhookDelete))
	mux.HandleFunc("POST /api/account/webhooks/{kind}/test", s.user(s.handleWebhookTest))

	mux.HandleFunc("GET /api/certs", s.user(s.handleCertList))
	mux.HandleFunc("POST /api/certs", s.user(s.handleCertCreate))
	mux.HandleFunc("POST /api/certs/{id}/renew", s.user(s.handleCertRenew))
	mux.HandleFunc("GET /api/certs/{id}", s.user(s.handleCertGet))
	mux.HandleFunc("GET /api/certs/{id}/files/{file}", s.user(s.handleCertFile))
	mux.HandleFunc("DELETE /api/certs/{id}", s.user(s.handleCertDelete))

	mux.HandleFunc("GET /api/admin/users", s.admin(s.handleAdminUsers))
	mux.HandleFunc("POST /api/admin/users/{id}/password", s.admin(s.handleAdminReset))

	// No method on purpose: every method gets 404 so the endpoint can't be discovered.
	mux.HandleFunc("/api/backup-all", s.handleBackupAll)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) { writeError(w, http.StatusNotFound, "Not found.") })
	if static != nil {
		mux.Handle("/", spa(static))
	}
	return mux
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	st := s.store.Load()
	if st == nil {
		http.Error(w, "database not connected", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := st.Ping(ctx); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte("ok"))
}

// handler types: open routes need only the database, user routes a session.
type (
	openHandler func(http.ResponseWriter, *http.Request, *store.Store)
	userHandler func(http.ResponseWriter, *http.Request, *store.Store, *Session)
)

func (s *Server) open(h openHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st := s.store.Load()
		if st == nil {
			writeError(w, http.StatusServiceUnavailable, "Database not available yet. Try again shortly.")
			return
		}
		h(w, r, st)
	}
}

func (s *Server) user(h userHandler) http.HandlerFunc {
	return s.open(func(w http.ResponseWriter, r *http.Request, st *store.Store) {
		sess, err := s.currentSession(r, st)
		if err != nil {
			serverError(w, r, err)
			return
		}
		if sess == nil {
			s.clearCookie(w)
			writeError(w, http.StatusUnauthorized, "Your session has expired. Please log in again.")
			return
		}
		h(w, r, st, sess)
	})
}

func (s *Server) admin(h userHandler) http.HandlerFunc {
	return s.user(func(w http.ResponseWriter, r *http.Request, st *store.Store, sess *Session) {
		if !sess.IsAdmin {
			writeError(w, http.StatusForbidden, "Admins only.")
			return
		}
		h(w, r, st, sess)
	})
}

// spa serves the built UI; unknown paths get index.html so client-side routes work.
func spa(static fs.FS) http.Handler {
	files := http.FileServerFS(static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "."
		}
		if _, err := fs.Stat(static, name); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}
