// Package flags generates per-instance dynamic flags. A per_instance challenge
// mints a unique flag per team instance; the value is a secret from birth and
// must never be logged, audited, or returned to a participant
// (docs/v0.2/05-flags.md).
package flags

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"
)

// crockford is Douglas Crockford's base32 alphabet (no I, L, O, U): unambiguous
// and human-legible if a participant pastes a flag back.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var encoding = base32.NewEncoding(crockford).WithPadding(base32.NoPadding)

// Generator mints flags of the form <prefix>{<39 crockford base32 chars>}.
type Generator struct{ prefix string }

// NewGenerator builds a generator; an empty prefix defaults to "osctf".
func NewGenerator(prefix string) *Generator {
	if prefix == "" {
		prefix = "osctf"
	}
	return &Generator{prefix: prefix}
}

// New mints a unique flag with >= 128 bits of entropy. The shape matches the
// house style so participants cannot tell a dynamic flag from a static one.
func (g *Generator) New() (string, error) {
	var b [24]byte // 192 bits -> 39 base32 chars
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("flags: reading entropy: %w", err)
	}
	return fmt.Sprintf("%s{%s}", g.prefix, strings.ToLower(encoding.EncodeToString(b[:]))), nil
}
