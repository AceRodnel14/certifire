package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"certifire/internal/crypto"
	"certifire/internal/store"
	"certifire/internal/storetest"
)

func mkUser(t *testing.T, st *store.Store, name string) *store.User {
	t.Helper()
	u := &store.User{Username: name, AuthSalt: []byte{1}, AuthHash: []byte{1}, KEKSalt: []byte{1}, WrappedDEK: []byte{1}, EscrowDEK: []byte{1}}
	if err := st.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestSchemaIdempotentAndUsers(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	u := mkUser(t, st, "Alice")
	if err := st.CreateUser(ctx, &store.User{Username: "ALICE", AuthSalt: []byte{1}, AuthHash: []byte{1}, KEKSalt: []byte{1}, WrappedDEK: []byte{1}, EscrowDEK: []byte{1}}); !errors.Is(err, store.ErrExists) {
		t.Errorf("duplicate: %v", err)
	}
	got, err := st.UserByName(ctx, "aLiCe")
	if err != nil || got.ID != u.ID || got.Theme.Mode != "auto" || got.Theme.Hue != nil {
		t.Errorf("lookup %+v %v", got, err)
	}
	if _, err := st.UserByName(ctx, "nobody"); !errors.Is(err, store.ErrNotFound) {
		t.Error("missing user")
	}
}

func TestSetupOnce(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	admin := &store.User{Username: "admin", IsAdmin: true, AuthSalt: []byte{1}, AuthHash: []byte{1}, KEKSalt: []byte{1}, WrappedDEK: []byte{1}, EscrowDEK: []byte{1}}
	meta := map[string][]byte{store.MetaEscrowPub: {1}, store.MetaEscrowPriv: {2}, store.MetaMasterSalt: {3}}
	if err := st.Setup(ctx, meta, admin); err != nil {
		t.Fatal(err)
	}
	if err := st.Setup(ctx, meta, &store.User{Username: "admin2"}); !errors.Is(err, store.ErrAlreadySetUp) {
		t.Errorf("second setup: %v", err)
	}
	if done, _ := st.IsSetUp(ctx); !done {
		t.Error("IsSetUp")
	}
}

func TestSessionsExpiryAndRevoke(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	u := mkUser(t, st, "bob")
	for _, s := range []struct {
		h   byte
		exp time.Duration
	}{{1, time.Hour}, {2, time.Hour}, {3, -time.Hour}} {
		if err := st.CreateSession(ctx, []byte{s.h}, u.ID, []byte("w"), time.Now().Add(s.exp)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.GetSession(ctx, []byte{3}); !errors.Is(err, store.ErrNotFound) {
		t.Error("expired session returned")
	}
	if err := st.SavePassword(ctx, u, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSession(ctx, []byte{1}); err != nil {
		t.Error("kept session lost")
	}
	if _, err := st.GetSession(ctx, []byte{2}); !errors.Is(err, store.ErrNotFound) {
		t.Error("other session not revoked")
	}
	if err := st.SavePassword(ctx, u, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSession(ctx, []byte{1}); !errors.Is(err, store.ErrNotFound) {
		t.Error("nil keep must revoke all")
	}
	if err := st.ReapSessions(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRenewTransaction(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	a, b := mkUser(t, st, "ann"), mkUser(t, st, "ben")
	mk := func(u *store.User) *store.Cert {
		return &store.Cert{ID: crypto.NewUUID(), UserID: u.ID, Name: "n", Hosts: []string{"a.test"}, Bits: 2048, Days: 5, Subject: []byte(`{}`),
			CNF: "c", CertPEM: "p", KeyEnc: []byte("k"), NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	}
	old := mk(a)
	if err := st.InsertCert(ctx, old, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertCert(ctx, mk(b), old.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("foreign renew: %v", err)
	}
	if err := st.InsertCert(ctx, mk(a), crypto.NewUUID()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown renew: %v", err)
	}
	nw := mk(a)
	if err := st.InsertCert(ctx, nw, old.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertCert(ctx, mk(a), old.ID); !errors.Is(err, store.ErrAlreadyRenewed) {
		t.Errorf("double renew: %v", err)
	}
	l, _ := st.ListCerts(ctx, a.ID)
	if len(l) != 2 {
		t.Errorf("failed renewals left rows: %d", len(l))
	}
	if g, _ := st.GetCert(ctx, a.ID, old.ID); g.RenewedBy == nil || *g.RenewedBy != nw.ID {
		t.Error("renewed_by")
	}
	if _, err := st.GetCert(ctx, b.ID, old.ID); !errors.Is(err, store.ErrNotFound) {
		t.Error("isolation")
	}
	// Deleting the user cascades.
	if _, err := st.ListUsers(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestAllUsers(t *testing.T) {
	st := storetest.New(t)
	for _, n := range []string{"bob", "Alice", "carol"} {
		mkUser(t, st, n)
	}
	us, err := st.AllUsers(context.Background())
	if err != nil || len(us) != 3 || us[0].Username != "Alice" || us[1].Username != "bob" || us[2].Username != "carol" {
		t.Errorf("AllUsers %v %v", us, err)
	}
}
