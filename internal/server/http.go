package server

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
)

const maxBody = 64 << 10

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func serverError(w http.ResponseWriter, r *http.Request, err error) {
	// err never carries secrets or webhook URLs (callers wrap only DB/crypto/openssl errors).
	slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "Something went wrong on the server.")
}

// readJSON decodes a JSON body (Content-Type must be application/json).
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "Expected a JSON body.")
		return false
	}
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		msg := "Invalid request."
		if strings.Contains(err.Error(), "too large") {
			msg = "Request too large."
		}
		writeError(w, http.StatusBadRequest, msg)
		return false
	}
	return true
}

// clientHost is the host part of RemoteAddr. X-Forwarded-* is never read.
func clientHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// limitBody caps every request body at 64 KiB.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next.ServeHTTP(w, r)
	})
}

// sameOrigin rejects cross-site state-changing requests. With SameSite=Lax
// cookies and the JSON-only bodies this is the CSRF protection. No CORS
// headers are ever sent, so browsers block cross-origin JSON requests.
//
// An Origin is accepted when its host equals the request Host or when it equals
// the scheme and host of baseURL (a proxy may rewrite Host).
func sameOrigin(baseURL string, next http.Handler) http.Handler {
	base := ""
	if u, err := url.Parse(baseURL); err == nil && u.Host != "" {
		base = u.Scheme + "://" + u.Host
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if o := r.Header.Get("Origin"); o != "" {
				if u, err := url.Parse(o); err != nil || (u.Host != r.Host && o != base) {
					writeError(w, http.StatusForbidden, "Cross-origin request rejected.")
					return
				}
			}
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				writeError(w, http.StatusForbidden, "Cross-origin request rejected.")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
