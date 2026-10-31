package server

import (
	"context"
	"crypto/ecdh"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"certifire/internal/crypto"
	"certifire/internal/store"
)

// ErrNotSetUp means no master key exists yet.
var ErrNotSetUp = errors.New("not set up yet")

// UnlockEscrow turns the master key into the escrow private key
// (crypto.ErrBadMasterKey if it is wrong).
func UnlockEscrow(ctx context.Context, st *store.Store, masterKey string) (*ecdh.PrivateKey, error) {
	salt, err := st.GetMeta(ctx, store.MetaMasterSalt)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotSetUp
	} else if err != nil {
		return nil, err
	}
	sealed, err := st.GetMeta(ctx, store.MetaEscrowPriv)
	if err != nil {
		return nil, err
	}
	return crypto.OpenEscrowPriv(masterKey, salt, sealed)
}

// ResetPassword recovers the user's DEK through escrow, re-wraps it under
// newPassword and revokes all of the user's sessions.
func ResetPassword(ctx context.Context, st *store.Store, masterKey, username, newPassword string) error {
	priv, err := UnlockEscrow(ctx, st, masterKey)
	if err != nil {
		return err
	}
	u, err := st.UserByName(ctx, username)
	if err != nil {
		return err
	}
	dek, err := crypto.EscrowOpen(priv, u.EscrowDEK, crypto.DEKAAD(u.Username))
	if err != nil {
		return fmt.Errorf("recover key for user %d: %w", u.ID, err)
	}
	k := crypto.WrapDEK(u.Username, newPassword, dek)
	u.AuthSalt, u.AuthHash, u.KEKSalt, u.WrappedDEK = k.AuthSalt, k.AuthHash, k.KEKSalt, k.WrappedDEK
	return st.SavePassword(ctx, u, nil)
}

// ValidateNewPassword exposes the password rules to the CLI.
func ValidateNewPassword(password, confirm string) string {
	return validateNewPassword(password, confirm)
}

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request, st *store.Store, _ *Session) {
	users, err := st.ListUsers(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) handleAdminReset(w http.ResponseWriter, r *http.Request, st *store.Store, sess *Session) {
	var req struct {
		MasterKey string `json:"masterKey"`
		Password  string `json:"password"`
		Confirm   string `json:"confirm"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "User not found.")
		return
	}
	target, err := st.UserByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "User not found.")
		return
	} else if err != nil {
		serverError(w, r, err)
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
	err = ResetPassword(r.Context(), st, strings.TrimSpace(req.MasterKey), target.Username, req.Password)
	if errors.Is(err, crypto.ErrBadMasterKey) {
		writeError(w, http.StatusForbidden, "That master key is not correct.")
		return
	}
	s.limiter.release(key)
	if err != nil {
		serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
