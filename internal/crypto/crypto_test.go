package crypto

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
)

func TestSealOpen(t *testing.T) {
	key := RandomBytes(KeyLen)
	ct := Seal(key, []byte("secret"), []byte("aad"))
	if got, err := Open(key, ct, []byte("aad")); err != nil || string(got) != "secret" {
		t.Fatalf("round trip: %q %v", got, err)
	}
	for name, tt := range map[string]struct {
		key, data, aad []byte
	}{
		"wrong aad": {key, ct, []byte("other")},
		"wrong key": {RandomBytes(KeyLen), ct, []byte("aad")},
		"truncated": {key, ct[:5], []byte("aad")},
		"tampered":  {key, append(bytes.Clone(ct[:len(ct)-1]), ct[len(ct)-1]^1), []byte("aad")},
	} {
		if _, err := Open(tt.key, tt.data, tt.aad); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if bytes.Equal(Seal(key, []byte("x"), nil), Seal(key, []byte("x"), nil)) {
		t.Error("nonce reused")
	}
}

func TestPasswordWrap(t *testing.T) {
	dek := RandomBytes(KeyLen)
	k := WrapDEK("Alice", "correct horse", dek)
	if !CheckPassword("correct horse", k.AuthSalt, k.AuthHash) || CheckPassword("wrong", k.AuthSalt, k.AuthHash) {
		t.Error("CheckPassword")
	}
	got, err := UnwrapDEK("alice", "correct horse", k.KEKSalt, k.WrappedDEK) // username is case-insensitive
	if err != nil || !bytes.Equal(got, dek) {
		t.Fatalf("unwrap: %v", err)
	}
	if _, err := UnwrapDEK("alice", "wrong", k.KEKSalt, k.WrappedDEK); err == nil {
		t.Error("wrong password unwrapped")
	}
	if _, err := UnwrapDEK("bob", "correct horse", k.KEKSalt, k.WrappedDEK); err == nil {
		t.Error("AAD does not bind the username")
	}
}

func TestEscrowAndMasterKey(t *testing.T) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	master, salt := RandomString(MasterKeyLen, MasterAlphabet), RandomBytes(SaltLen)
	sealed := SealEscrowPriv(master, salt, priv)

	dek := RandomBytes(KeyLen)
	esc, err := EscrowSeal(priv.PublicKey(), dek, DEKAAD("alice"))
	if err != nil {
		t.Fatal(err)
	}
	opened, err := OpenEscrowPriv(master, salt, sealed)
	if err != nil {
		t.Fatal(err)
	}
	got, err := EscrowOpen(opened, esc, DEKAAD("alice"))
	if err != nil || !bytes.Equal(got, dek) {
		t.Fatalf("escrow open: %v", err)
	}
	if _, err := EscrowOpen(opened, esc, DEKAAD("bob")); err == nil {
		t.Error("escrow AAD not enforced")
	}
	if _, err := EscrowOpen(opened, esc[:10], nil); err == nil {
		t.Error("short escrow accepted")
	}
	if _, err := OpenEscrowPriv("wrong-master-key-0000", salt, sealed); !errors.Is(err, ErrBadMasterKey) {
		t.Errorf("wrong master key: %v", err)
	}
}

func TestRandomHelpers(t *testing.T) {
	mk := RandomString(MasterKeyLen, MasterAlphabet)
	if len(mk) != MasterKeyLen || strings.ContainsAny(mk, "0OoIl1") {
		t.Errorf("master key %q", mk)
	}
	if a, b := NewUUID(), NewUUID(); a == b || len(a) != 36 || a[14] != '4' {
		t.Errorf("uuid %q %q", a, b)
	}
	if string(CertKeyAAD(7, "abc")) != "cert-key:7:abc" {
		t.Error("cert key AAD format")
	}
}
