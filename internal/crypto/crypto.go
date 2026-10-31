// Package crypto holds CertiFire's key hierarchy primitives.
//
//	user password --argon2id(auth_salt)--> auth_hash   stored; verifies login only
//	user password --argon2id(kek_salt)---> KEK         never stored
//	KEK          --AES-GCM--> wrapped_dek              stored
//	escrow pub   --X25519+AES-GCM--> escrow_dek        stored; lets the master key recover the DEK
//	DEK          --AES-GCM--> every private key
//
//	master key   --argon2id(master_salt)--> MK         never stored
//	MK           --AES-GCM--> escrow private key       stored
//
// The database alone can't decrypt a private key. Recovering a user's DEK
// requires their password or the master key.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 2

	// KeyLen is the length of every symmetric key.
	KeyLen = 32
	// SaltLen is the length of every salt.
	SaltLen = 16

	// MasterAlphabet is alphanumeric without look-alikes (0/O/o, 1/l/I) so the
	// master key can be copied by hand. 20 characters are about 116 bits.
	MasterAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789"
	// MasterKeyLen is the master key length.
	MasterKeyLen = 20
)

// RandomBytes returns n random bytes.
func RandomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return b
}

// DeriveKey derives a 32-byte key from a secret with Argon2id.
func DeriveKey(secret string, salt []byte) []byte {
	return argon2.IDKey([]byte(secret), salt, argonTime, argonMemory, argonThreads, KeyLen)
}

// CheckPassword compares a password against a stored auth hash in constant time.
func CheckPassword(password string, salt, hash []byte) bool {
	return subtle.ConstantTimeCompare(DeriveKey(password, salt), hash) == 1
}

// SubKey derives a purpose-bound key from secret with HKDF-SHA256.
func SubKey(secret []byte, info string) []byte {
	k, err := hkdf.Key(sha256.New, secret, nil, info, KeyLen)
	if err != nil {
		panic(err)
	}
	return k
}

// Seal encrypts with AES-256-GCM; the random nonce is prepended. aad binds a
// ciphertext to its row so rows cannot be swapped.
func Seal(key, plaintext, aad []byte) []byte {
	gcm := newGCM(key)
	nonce := RandomBytes(gcm.NonceSize())
	return gcm.Seal(nonce, nonce, plaintext, aad)
}

// Open reverses Seal.
func Open(key, data, aad []byte) ([]byte, error) {
	gcm := newGCM(key)
	if len(data) < gcm.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	return gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], aad)
}

func newGCM(key []byte) cipher.AEAD {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err) // wrong key length is a programming error
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return gcm
}

const escrowInfo = "certifire escrow"

// EscrowSeal encrypts data to the escrow public key (ephemeral X25519 +
// AES-GCM), so a new user's DEK can be escrowed without the master key.
func EscrowSeal(pub *ecdh.PublicKey, data, aad []byte) ([]byte, error) {
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	shared, err := eph.ECDH(pub)
	if err != nil {
		return nil, err
	}
	ephPub := eph.PublicKey().Bytes()
	key := SubKey(append(shared, ephPub...), escrowInfo)
	return append(ephPub, Seal(key, data, aad)...), nil
}

// EscrowOpen reverses EscrowSeal with the escrow private key.
func EscrowOpen(priv *ecdh.PrivateKey, data, aad []byte) ([]byte, error) {
	if len(data) < 32 {
		return nil, errors.New("escrow data too short")
	}
	ephPub, err := ecdh.X25519().NewPublicKey(data[:32])
	if err != nil {
		return nil, err
	}
	shared, err := priv.ECDH(ephPub)
	if err != nil {
		return nil, err
	}
	key := SubKey(append(shared, data[:32]...), escrowInfo)
	return Open(key, data[32:], aad)
}

// RandomString returns n characters drawn uniformly from alphabet.
func RandomString(n int, alphabet string) string {
	out := make([]byte, n)
	m := big.NewInt(int64(len(alphabet)))
	for i := range out {
		v, err := rand.Int(rand.Reader, m)
		if err != nil {
			panic(err)
		}
		out[i] = alphabet[v.Int64()]
	}
	return string(out)
}

// NewUUID returns a random version-4 UUID.
func NewUUID() string {
	b := RandomBytes(16)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// AAD helpers: every ciphertext is bound to the row it belongs to.

// DEKAAD binds a wrapped or escrowed DEK to its user.
func DEKAAD(username string) []byte { return []byte("dek:" + strings.ToLower(username)) }

// SessionAAD binds a session's wrapped DEK to its user.
func SessionAAD(userID int64) []byte { return []byte(fmt.Sprintf("session:%d", userID)) }

// CertKeyAAD binds an encrypted private key to its user and certificate.
func CertKeyAAD(userID int64, certID string) []byte {
	return []byte(fmt.Sprintf("cert-key:%d:%s", userID, certID))
}

// SessionKey derives the key that wraps a session's DEK from the cookie token.
func SessionKey(token []byte) []byte { return SubKey(token, "certifire session") }

// PasswordKeys is the stored material derived from a password.
type PasswordKeys struct {
	AuthSalt, AuthHash, KEKSalt, WrappedDEK []byte
}

// WrapDEK derives fresh salts from password and wraps dek under the new KEK.
func WrapDEK(username, password string, dek []byte) PasswordKeys {
	k := PasswordKeys{AuthSalt: RandomBytes(SaltLen), KEKSalt: RandomBytes(SaltLen)}
	k.AuthHash = DeriveKey(password, k.AuthSalt)
	k.WrappedDEK = Seal(DeriveKey(password, k.KEKSalt), dek, DEKAAD(username))
	return k
}

// UnwrapDEK recovers the DEK with the user's password.
func UnwrapDEK(username, password string, kekSalt, wrapped []byte) ([]byte, error) {
	return Open(DeriveKey(password, kekSalt), wrapped, DEKAAD(username))
}

var escrowPrivAAD = []byte("escrow_priv")

// SealEscrowPriv seals the escrow private key under the master key.
func SealEscrowPriv(masterKey string, masterSalt []byte, priv *ecdh.PrivateKey) []byte {
	return Seal(DeriveKey(masterKey, masterSalt), priv.Bytes(), escrowPrivAAD)
}

// ErrBadMasterKey means the master key did not open the escrow private key.
var ErrBadMasterKey = errors.New("invalid master key")

// OpenEscrowPriv recovers the escrow private key from the master key.
func OpenEscrowPriv(masterKey string, masterSalt, sealed []byte) (*ecdh.PrivateKey, error) {
	raw, err := Open(DeriveKey(masterKey, masterSalt), sealed, escrowPrivAAD)
	if err != nil {
		return nil, ErrBadMasterKey
	}
	return ecdh.X25519().NewPrivateKey(raw)
}
