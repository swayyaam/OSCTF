package teams

import (
	"crypto/rand"
	"fmt"
)

// crockford base32 alphabet (no I, L, O, U — avoids ambiguity).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

const inviteCodeLen = 12

// GenerateInviteCode returns a 12-character Crockford base32 invite code.
func GenerateInviteCode() (string, error) {
	buf := make([]byte, inviteCodeLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("teams: generating invite code: %w", err)
	}
	for i, b := range buf {
		buf[i] = crockford[int(b)%len(crockford)]
	}
	return string(buf), nil
}
