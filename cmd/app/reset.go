package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"certifire/internal/crypto"
	"certifire/internal/server"
	"certifire/internal/store"

	"golang.org/x/term"
)

// resetPassword implements `app reset-password`. It prompts (never argv) for
// the master key, username, new password and confirmation, then re-wraps that
// user's data key via escrow and revokes their sessions. Not throttled: it runs
// locally with database access already.
func resetPassword(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: app reset-password   (prompts for the master key; it is never taken from arguments)")
		return 2
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "error: DATABASE_URL is not set")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	st, err := store.Open(ctx, dbURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: cannot reach the database:", err)
		return 1
	}
	defer st.Close()

	in := bufio.NewReader(os.Stdin)
	masterKey := strings.TrimSpace(prompt(in, "Master key: ", true))
	if _, err := server.UnlockEscrow(ctx, st, masterKey); errors.Is(err, crypto.ErrBadMasterKey) {
		fmt.Fprintln(os.Stderr, "error: that master key is not correct")
		return 1
	} else if errors.Is(err, server.ErrNotSetUp) {
		fmt.Fprintln(os.Stderr, "error: CertiFire has not been set up yet")
		return 1
	} else if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	username := strings.TrimSpace(prompt(in, "Username: ", false))
	if _, err := st.UserByName(ctx, username); errors.Is(err, store.ErrNotFound) {
		fmt.Fprintln(os.Stderr, "error: no such user")
		return 1
	} else if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	password := prompt(in, "New password: ", true)
	confirm := prompt(in, "Confirm new password: ", true)
	if msg := server.ValidateNewPassword(password, confirm); msg != "" {
		fmt.Fprintln(os.Stderr, "error:", msg)
		return 1
	}
	if err := server.ResetPassword(ctx, st, masterKey, username, password); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Println("Password reset. The user's certificates and keys are intact; their sessions were signed out.")
	return 0
}

func prompt(in *bufio.Reader, label string, secret bool) string {
	fmt.Print(label)
	if secret && term.IsTerminal(int(os.Stdin.Fd())) {
		b, _ := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		return string(b)
	}
	line, _ := in.ReadString('\n')
	return strings.TrimRight(line, "\r\n")
}
