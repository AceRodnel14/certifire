package server

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"certifire/internal/crypto"
	"certifire/internal/store"
)

const minPasswordLen = 8

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{3,32}$`)

// Session is an authenticated request's identity and unwrapped data key.
type Session struct {
	UserID    int64
	Username  string
	IsAdmin   bool
	Theme     store.Theme
	DEK       []byte
	TokenHash []byte
}

// validateNewPassword returns a user-facing message, or "" when the pair is fine.
func validateNewPassword(password, confirm string) string {
	if len(password) < minPasswordLen {
		return fmt.Sprintf("Password must be at least %d characters.", minPasswordLen)
	}
	if password != confirm {
		return "Passwords do not match."
	}
	return ""
}

func (s *Server) createSession(ctx context.Context, st *store.Store, w http.ResponseWriter, userID int64, dek []byte) error {
	token := crypto.RandomBytes(32)
	hash := sha256.Sum256(token)
	expires := time.Now().Add(sessionTTL)
	wrapped := crypto.Seal(crypto.SessionKey(token), dek, crypto.SessionAAD(userID))
	if err := st.CreateSession(ctx, hash[:], userID, wrapped, expires); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: base64.RawURLEncoding.EncodeToString(token),
		Path: "/", Expires: expires, HttpOnly: true, Secure: s.cfg.SecureCookie(), SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (s *Server) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.cfg.SecureCookie(), SameSite: http.SameSiteLaxMode})
}

func cookieToken(r *http.Request) []byte {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	token, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil || len(token) != 32 {
		return nil
	}
	return token
}

// currentSession returns nil (no error) for a missing, expired or unreadable session.
func (s *Server) currentSession(r *http.Request, st *store.Store) (*Session, error) {
	token := cookieToken(r)
	if token == nil {
		return nil, nil
	}
	hash := sha256.Sum256(token)
	row, err := st.GetSession(r.Context(), hash[:])
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	dek, err := crypto.Open(crypto.SessionKey(token), row.WrappedDEK, crypto.SessionAAD(row.UserID))
	if err != nil {
		return nil, nil
	}
	return &Session{UserID: row.UserID, Username: row.Username, IsAdmin: row.IsAdmin, Theme: row.Theme, DEK: dek, TokenHash: hash[:]}, nil
}

type userView struct {
	Username string      `json:"username"`
	IsAdmin  bool        `json:"isAdmin"`
	Theme    store.Theme `json:"theme"`
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request, st *store.Store) {
	done, err := st.IsSetUp(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	resp := map[string]any{"setupDone": done, "user": nil}
	sess, err := s.currentSession(r, st)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if sess != nil {
		resp["user"] = userView{sess.Username, sess.IsAdmin, sess.Theme}
	}
	writeJSON(w, http.StatusOK, resp)
}

// newUser builds the key material for a fresh account.
func newUser(username, password string, isAdmin bool, pub *ecdh.PublicKey) (*store.User, []byte, error) {
	dek := crypto.RandomBytes(crypto.KeyLen)
	k := crypto.WrapDEK(username, password, dek)
	escrow, err := crypto.EscrowSeal(pub, dek, crypto.DEKAAD(username))
	if err != nil {
		return nil, nil, err
	}
	return &store.User{
		Username: username, IsAdmin: isAdmin,
		AuthSalt: k.AuthSalt, AuthHash: k.AuthHash, KEKSalt: k.KEKSalt, WrappedDEK: k.WrappedDEK, EscrowDEK: escrow,
		Theme: store.Theme{Mode: "auto"},
	}, dek, nil
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request, st *store.Store) {
	var req struct{ Password, Confirm string }
	if !readJSON(w, r, &req) {
		return
	}
	if msg := validateNewPassword(req.Password, req.Confirm); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if done, err := st.IsSetUp(r.Context()); err != nil {
		serverError(w, r, err)
		return
	} else if done {
		writeError(w, http.StatusConflict, "CertiFire is already set up.")
		return
	}
	if !s.kdf.tryAcquire() {
		writeError(w, http.StatusTooManyRequests, "Busy, try again.")
		return
	}
	defer s.kdf.release()

	masterKey := crypto.RandomString(crypto.MasterKeyLen, crypto.MasterAlphabet)
	masterSalt := crypto.RandomBytes(crypto.SaltLen)
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		serverError(w, r, err)
		return
	}
	admin, _, err := newUser("admin", req.Password, true, priv.PublicKey())
	if err != nil {
		serverError(w, r, err)
		return
	}
	err = st.Setup(r.Context(), map[string][]byte{
		store.MetaMasterSalt: masterSalt,
		store.MetaEscrowPriv: crypto.SealEscrowPriv(masterKey, masterSalt, priv),
		store.MetaEscrowPub:  priv.PublicKey().Bytes(),
	}, admin)
	if errors.Is(err, store.ErrAlreadySetUp) {
		writeError(w, http.StatusConflict, "CertiFire is already set up.")
		return
	} else if err != nil {
		serverError(w, r, err)
		return
	}
	// No session is started: the UI shows Login after the master key.
	writeJSON(w, http.StatusCreated, map[string]string{"masterKey": masterKey})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request, st *store.Store) {
	var req struct{ Username, Password, Confirm string }
	if !readJSON(w, r, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if !usernameRe.MatchString(req.Username) {
		writeError(w, http.StatusBadRequest, "Username must be 3-32 characters: letters, numbers, dot, dash or underscore.")
		return
	}
	if msg := validateNewPassword(req.Password, req.Confirm); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	raw, err := st.GetMeta(r.Context(), store.MetaEscrowPub)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusConflict, "CertiFire has not been set up yet.")
		return
	} else if err != nil {
		serverError(w, r, err)
		return
	}
	pub, err := ecdh.X25519().NewPublicKey(raw)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if !s.kdf.tryAcquire() {
		writeError(w, http.StatusTooManyRequests, "Busy, try again.")
		return
	}
	defer s.kdf.release()
	u, dek, err := newUser(req.Username, req.Password, false, pub)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if err := st.CreateUser(r.Context(), u); errors.Is(err, store.ErrExists) {
		writeError(w, http.StatusConflict, "That username is already taken.")
		return
	} else if err != nil {
		serverError(w, r, err)
		return
	}
	if err := s.createSession(r.Context(), st, w, u.ID, dek); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, userView{u.Username, false, u.Theme})
}

// attemptKey is the throttle key: lowercased username plus the client host.
func attemptKey(username string, r *http.Request) string {
	return strings.ToLower(strings.TrimSpace(username)) + "|" + clientHost(r)
}

var errBadLogin = errors.New("wrong username or password")

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request, st *store.Store) {
	var req struct{ Username, Password string }
	if !readJSON(w, r, &req) {
		return
	}
	key := attemptKey(req.Username, r)
	if !s.kdf.tryAcquire() {
		writeError(w, http.StatusTooManyRequests, "Busy, try again.")
		return
	}
	defer s.kdf.release()
	// Record the attempt before the slow derivation so concurrent guesses
	// count; a success releases it. At the limit the answer is 429 even for
	// the right password.
	if !s.limiter.take(key) {
		writeError(w, http.StatusTooManyRequests, "Too many failed attempts. Try again in a few minutes.")
		return
	}
	u, err := st.UserByName(r.Context(), strings.TrimSpace(req.Username))
	var dek []byte
	switch {
	case errors.Is(err, store.ErrNotFound):
		crypto.DeriveKey(req.Password, make([]byte, crypto.SaltLen)) // similar timing for unknown users
		err = errBadLogin
	case err == nil && !crypto.CheckPassword(req.Password, u.AuthSalt, u.AuthHash):
		err = errBadLogin
	case err == nil:
		dek, err = crypto.UnwrapDEK(u.Username, req.Password, u.KEKSalt, u.WrappedDEK)
		if err != nil {
			err = fmt.Errorf("unwrap key for user %d: %w", u.ID, err)
		}
	}
	if errors.Is(err, errBadLogin) {
		writeError(w, http.StatusUnauthorized, "Wrong username or password.")
		return
	} else if err != nil {
		s.limiter.release(key)
		serverError(w, r, err)
		return
	}
	s.limiter.release(key)
	if err := s.createSession(r.Context(), st, w, u.ID, dek); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, userView{u.Username, u.IsAdmin, u.Theme})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, st *store.Store) {
	if token := cookieToken(r); token != nil {
		hash := sha256.Sum256(token)
		if err := st.DeleteSession(r.Context(), hash[:]); err != nil {
			slog.Error("logout: delete session", "err", err)
		}
	}
	s.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request, st *store.Store, sess *Session) {
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		Password        string `json:"password"`
		Confirm         string `json:"confirm"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if msg := validateNewPassword(req.Password, req.Confirm); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	key := attemptKey(sess.Username, r)
	if !s.kdf.tryAcquire() {
		writeError(w, http.StatusTooManyRequests, "Busy, try again.")
		return
	}
	defer s.kdf.release()
	if !s.limiter.take(key) {
		writeError(w, http.StatusTooManyRequests, "Too many failed attempts. Try again in a few minutes.")
		return
	}
	u, err := st.UserByID(r.Context(), sess.UserID)
	if err != nil {
		s.limiter.release(key)
		serverError(w, r, err)
		return
	}
	if !crypto.CheckPassword(req.CurrentPassword, u.AuthSalt, u.AuthHash) {
		writeError(w, http.StatusUnauthorized, "Current password is wrong.")
		return
	}
	s.limiter.release(key)
	k := crypto.WrapDEK(u.Username, req.Password, sess.DEK) // same DEK, new KEK; escrow unchanged
	u.AuthSalt, u.AuthHash, u.KEKSalt, u.WrappedDEK = k.AuthSalt, k.AuthHash, k.KEKSalt, k.WrappedDEK
	if err := st.SavePassword(r.Context(), u, sess.TokenHash); err != nil {
		serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTheme(w http.ResponseWriter, r *http.Request, st *store.Store, sess *Session) {
	var in struct {
		Mode string `json:"mode"`
		Hue  *int   `json:"hue"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	t := store.Theme{Mode: in.Mode}
	switch in.Mode {
	case "auto", "light", "dark":
	default:
		writeError(w, http.StatusBadRequest, "Invalid theme mode.")
		return
	}
	if in.Hue != nil {
		if *in.Hue < 0 || *in.Hue > 359 {
			writeError(w, http.StatusBadRequest, "Hue must be 0 to 359.")
			return
		}
		h := int16(*in.Hue)
		t.Hue = &h
	}
	if err := st.SetTheme(r.Context(), sess.UserID, t); err != nil {
		serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
