package server

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	yzip "github.com/yeka/zip"

	"certifire/internal/crypto"
	"certifire/internal/store"
)

const (
	backupMaxFails    = 5
	backupWindow      = 15 * time.Minute
	minBackupPassword = 16 // one password protects every user's keys
)

// forwardingHeaders mark a request that was relayed by a proxy.
var forwardingHeaders = []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip", "Forwarded"}

// handleBackupAll returns every user's certificates in one AES-256 zip,
// unlocked with the master key. It only serves direct connections from the
// allow-list and answers 404 otherwise (any method), so it can't be discovered.
func (s *Server) handleBackupAll(w http.ResponseWriter, r *http.Request) {
	notFound := func() { writeError(w, http.StatusNotFound, "Not found.") }
	if len(s.cfg.BackupAllIPs) == 0 {
		notFound()
		return
	}
	peer := netip.Addr{}
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		peer = ap.Addr().Unmap()
	}
	ip := "unknown"
	if peer.IsValid() {
		ip = peer.String()
	}
	logf := func(level slog.Level, result string, args ...any) {
		slog.Log(r.Context(), level, "backup-all", append([]any{"result", result, "ip", ip}, args...)...)
	}
	// Presence only: header values are never used.
	for _, h := range forwardingHeaders {
		if len(r.Header.Values(h)) > 0 {
			logf(slog.LevelWarn, "denied", "reason", "proxy")
			notFound()
			return
		}
	}
	if !peer.IsValid() || !inPrefixes(peer, s.cfg.BackupAllIPs) || r.Method != http.MethodPost {
		logf(slog.LevelWarn, "denied", "reason", "ip-or-method")
		notFound()
		return
	}
	st := s.store.Load()
	if st == nil {
		logf(slog.LevelWarn, "unavailable")
		writeError(w, http.StatusServiceUnavailable, "Database not available yet. Try again shortly.")
		return
	}
	var req struct {
		MasterKey      string `json:"masterKey"`
		BackupPassword string `json:"backupPassword"`
	}
	if !readJSON(w, r, &req) {
		logf(slog.LevelWarn, "bad-request")
		return
	}
	req.MasterKey = strings.TrimSpace(req.MasterKey)
	if req.MasterKey == "" || utf8.RuneCountInString(req.BackupPassword) < minBackupPassword {
		logf(slog.LevelWarn, "bad-request")
		writeError(w, http.StatusBadRequest, fmt.Sprintf("masterKey is required and backupPassword must be at least %d characters.", minBackupPassword))
		return
	}
	if !s.backupAll.tryAcquire() {
		logf(slog.LevelWarn, "busy")
		writeError(w, http.StatusTooManyRequests, "Busy, try again.")
		return
	}
	defer s.backupAll.release()
	// Attempts are recorded before the slow unlock so concurrent guesses are
	// counted; they are released again on success or server error.
	if !s.backupIPLimiter.take(ip) {
		logf(slog.LevelWarn, "throttled")
		writeError(w, http.StatusTooManyRequests, "Too many failed attempts. Try again later.")
		return
	}
	if !s.backupAllLimiter.take("*") {
		s.backupIPLimiter.release(ip)
		logf(slog.LevelWarn, "throttled")
		writeError(w, http.StatusTooManyRequests, "Too many failed attempts. Try again later.")
		return
	}
	zipped, users, certs, err := buildBackupAll(r.Context(), st, req.MasterKey, req.BackupPassword)
	if errors.Is(err, crypto.ErrBadMasterKey) || errors.Is(err, ErrNotSetUp) {
		logf(slog.LevelWarn, "bad-key")
		writeError(w, http.StatusUnauthorized, "That master key is not correct.")
		return
	} else if err != nil {
		s.backupIPLimiter.release(ip)
		s.backupAllLimiter.release("*")
		logf(slog.LevelError, "error")
		serverError(w, r, err)
		return
	}
	s.backupIPLimiter.release(ip)
	s.backupAllLimiter.release("*")
	logf(slog.LevelInfo, "ok", "users", users, "certs", certs)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="certifire-backup-all_`+time.Now().Format("2006-01-02")+`.zip"`)
	_, _ = w.Write(zipped)
}

// inPrefixes reports whether a is inside any of list.
func inPrefixes(a netip.Addr, list []netip.Prefix) bool {
	a = a.Unmap().WithZone("")
	for _, p := range list {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// buildBackupAll recovers every user's DEK through escrow and returns the
// AES-256 zip holding certifire-all.zip, which holds one <username>.zip per
// user with each cert's .cnf, .crt and .key. Any failure aborts the whole run.
func buildBackupAll(ctx context.Context, st *store.Store, masterKey, backupPassword string) (zipped []byte, users, certs int, err error) {
	priv, err := UnlockEscrow(ctx, st, masterKey)
	if err != nil {
		return nil, 0, 0, err
	}
	all, err := st.AllUsers(ctx)
	if err != nil {
		return nil, 0, 0, err
	}
	perUser := map[string][]byte{}
	for _, u := range all {
		dek, err := crypto.EscrowOpen(priv, u.EscrowDEK, crypto.DEKAAD(u.Username))
		if err != nil {
			return nil, 0, 0, fmt.Errorf("recover key for user %d: %w", u.ID, err)
		}
		list, err := st.ListCerts(ctx, u.ID)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("list certs of user %d: %w", u.ID, err)
		}
		bases := fileBases(list)
		files := map[string][]byte{}
		for i, c := range list {
			key, err := crypto.Open(dek, c.KeyEnc, crypto.CertKeyAAD(u.ID, c.ID))
			if err != nil {
				return nil, 0, 0, fmt.Errorf("open key of cert %s of user %d: %w", c.ID, u.ID, err)
			}
			files[bases[i]+".cnf"] = []byte(c.CNF)
			files[bases[i]+".crt"] = []byte(c.CertPEM)
			files[bases[i]+".key"] = key
		}
		b, err := plainZip(files)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("zip user %d: %w", u.ID, err)
		}
		perUser[u.Username+".zip"] = b
		certs += len(list)
	}
	inner, err := plainZip(perUser)
	if err != nil {
		return nil, 0, 0, err
	}
	// The inner zip is the only entry, so usernames and sizes stay hidden
	// without the backup password.
	var buf bytes.Buffer
	zw := yzip.NewWriter(&buf)
	fw, err := zw.Encrypt("certifire-all.zip", backupPassword, yzip.AES256Encryption)
	if err != nil {
		return nil, 0, 0, err
	}
	if _, err := fw.Write(inner); err != nil {
		return nil, 0, 0, err
	}
	if err := zw.Close(); err != nil {
		return nil, 0, 0, err
	}
	return buf.Bytes(), len(all), certs, nil
}

// plainZip packs files into an unencrypted zip, in name order.
func plainZip(files map[string][]byte) ([]byte, error) {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range names {
		fw, err := zw.Create(n)
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write(files[n]); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// fileBases picks a unique file name base for each cert, in order (newest
// first). Names are not unique per user (renewals keep theirs): the first cert
// with a name gets <name>, later ones <name>_<cert id>, and if that is also
// taken a _2, _3 ... suffix is appended.
func fileBases(certs []*store.Cert) []string {
	used := make(map[string]bool, len(certs))
	out := make([]string, len(certs))
	for i, c := range certs {
		// Keyed lower-case: names differing only in case collide on
		// case-insensitive filesystems.
		base := c.Name
		if used[strings.ToLower(base)] {
			base = c.Name + "_" + c.ID
			for n := 2; used[strings.ToLower(base)]; n++ {
				base = c.Name + "_" + c.ID + "_" + strconv.Itoa(n)
			}
		}
		used[strings.ToLower(base)] = true
		out[i] = base
	}
	return out
}
