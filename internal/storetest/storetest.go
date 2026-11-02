// Package storetest gives tests an isolated schema in the database named by
// TEST_DATABASE_URL. It is imported by tests only.
package storetest

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"certifire/internal/crypto"
	"certifire/internal/store"

	"github.com/jackc/pgx/v5"
)

// New returns a Store on a fresh schema, dropped at cleanup. The test is
// skipped when TEST_DATABASE_URL is unset.
func New(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}

// URL returns a database URL whose search_path is a fresh, empty schema,
// dropped at cleanup. The test is skipped when TEST_DATABASE_URL is unset.
func URL(t *testing.T) string {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	schema := "t_" + strings.ToLower(crypto.RandomString(12, "abcdefghijklmnopqrstuvwxyz"))
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close(ctx)
	})
	return u.String()
}
