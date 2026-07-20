// Package auth owns credentials and sessions: argon2id password hashing (PHC
// strings), the AuthProvider interface with the v0.1 email+password
// implementation, Redis-backed sessions, and the request identity context.
// Parameters per docs/v0.1/06-auth.md.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters (docs/v0.1/06-auth.md). Changing them re-hashes stored
// passwords transparently on the next successful login (NeedsRehash).
const (
	argonMemoryKiB  = 64 * 1024
	argonIterations = 3
	argonThreads    = 4
	argonSaltLen    = 16
	argonKeyLen     = 32
)

// ErrInvalidHash marks a stored hash that cannot be parsed as a PHC argon2id string.
var ErrInvalidHash = errors.New("auth: invalid password hash")

// HashPassword derives an argon2id hash and encodes it as a PHC string:
// $argon2id$v=19$m=65536,t=3,p=4$<b64 salt>$<b64 hash>
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: generating salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonIterations, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonIterations, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

type phcParams struct {
	memoryKiB  uint32
	iterations uint32
	threads    uint8
	salt       []byte
	hash       []byte
}

func parsePHC(encoded string) (phcParams, error) {
	var p phcParams
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return p, ErrInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return p, ErrInvalidHash
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memoryKiB, &p.iterations, &p.threads); err != nil {
		return p, ErrInvalidHash
	}
	var err error
	if p.salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return p, ErrInvalidHash
	}
	if p.hash, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil {
		return p, ErrInvalidHash
	}
	return p, nil
}

// VerifyPassword recomputes the hash with the parameters stored in the PHC
// string and compares in constant time.
func VerifyPassword(password, encoded string) (bool, error) {
	p, err := parsePHC(encoded)
	if err != nil {
		return false, err
	}
	//nolint:gosec // G115: hash length is a fixed 32 bytes, never overflows uint32.
	key := argon2.IDKey([]byte(password), p.salt, p.iterations, p.memoryKiB, p.threads, uint32(len(p.hash)))
	return subtle.ConstantTimeCompare(key, p.hash) == 1, nil
}

// NeedsRehash reports whether the stored hash uses parameters different from the
// current configuration (upgrade path: re-hash on next successful login).
func NeedsRehash(encoded string) bool {
	p, err := parsePHC(encoded)
	if err != nil {
		return true
	}
	return p.memoryKiB != argonMemoryKiB || p.iterations != argonIterations || p.threads != argonThreads
}

// dummyHash is a fixed valid PHC hash of an unguessable string, used to equalize
// login timing when the email does not exist (docs/v0.1/06-auth.md).
var dummyHash = func() string {
	h, err := HashPassword("osctf-dummy-timing-uniformity")
	if err != nil {
		panic(err) // crypto/rand failure at init is unrecoverable
	}
	return h
}()

// BurnHash performs one argon2 verification against the dummy hash so a login
// with an unknown email costs the same as one with a known email.
func BurnHash(password string) {
	_, _ = VerifyPassword(password, dummyHash)
}
