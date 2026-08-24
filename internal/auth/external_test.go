package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/swayyaam/OSCTF/plugin/sdk/contract"
)

// discardLogger keeps rejection logging out of test output; the logging itself is behaviour the
// integration tests assert on, not something these unit tests read.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestParseProvisionPolicy(t *testing.T) {
	for _, in := range []string{"open", "OPEN", " invite-only ", "off"} {
		if _, err := ParseProvisionPolicy(in); err != nil {
			t.Errorf("ParseProvisionPolicy(%q): unexpected error %v", in, err)
		}
	}
	// An unrecognised value must be an error, never a silent default — defaulting here would
	// choose a security posture for the operator.
	for _, in := range []string{"", "invite_only", "openish", "true"} {
		if _, err := ParseProvisionPolicy(in); err == nil {
			t.Errorf("ParseProvisionPolicy(%q): want error, got none", in)
		}
	}
}

// An externally-provisioned account stores a password that cannot parse as PHC, so the ordinary
// credential path rejects it as invalid credentials rather than erroring. If this ever verifies,
// every SSO account gains a password anyone could guess the encoding of.
func TestNoPasswordHashNeverVerifies(t *testing.T) {
	for _, pw := range []string{"", "!", "password", noPasswordHash, "hunter2"} {
		ok, err := VerifyPassword(context.Background(), pw, noPasswordHash)
		if ok {
			t.Fatalf("VerifyPassword(%q, noPasswordHash) = true; an SSO account must not be password-loginable", pw)
		}
		if err == nil {
			t.Errorf("VerifyPassword(%q, noPasswordHash): want a parse error, got nil", pw)
		}
	}
}

func TestSanitizeUsername(t *testing.T) {
	cases := []struct{ in, want string }{
		{"alice", "alice"},
		{"  Alice.B-1_ ", "Alice.B-1_"},           // surrounding space trimmed
		{"alice@example.com", "aliceexample.com"}, // '@' dropped, the rest is legal
		{"日本語", ""},                               // nothing legal survives
		{"ab", ""},                                // under the 3-char minimum
		{"<script>alert(1)</script>", "scriptalert1script"},
		{strings.Repeat("a", 50), strings.Repeat("a", 32)}, // truncated to the max
	}
	for _, tc := range cases {
		if got := sanitizeUsername(tc.in); got != tc.want {
			t.Errorf("sanitizeUsername(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCandidateUsernameStaysWithinLimit(t *testing.T) {
	base := strings.Repeat("a", usernameMaxLen)
	for attempt := range provisionAttempts {
		got := candidateUsername(base, attempt)
		if len(got) > usernameMaxLen {
			t.Errorf("attempt %d: %q is %d chars, over the %d limit", attempt, got, len(got), usernameMaxLen)
		}
		if !usernameRe.MatchString(got) {
			t.Errorf("attempt %d: %q does not satisfy the username rule", attempt, got)
		}
	}
	if a, b := candidateUsername("bob", 0), candidateUsername("bob", 1); a == b {
		t.Errorf("attempts must differ, both were %q", a)
	}
}

// Malformed and authority-asserting claims are refused BEFORE any database work. The resolver is
// built with nil queries on purpose: if any of these paths ever starts touching the database,
// this test panics rather than quietly passing.
func TestResolveRejectsBadClaimsWithoutTouchingTheDatabase(t *testing.T) {
	r := NewExternalResolver(nil, ProvisionOpen, discardLogger())
	cases := []struct {
		name     string
		provider string
		id       ExternalIdentity
	}{
		{"empty provider", "", ExternalIdentity{Subject: "s"}},
		{"no subject", "oidc", ExternalIdentity{Subject: "   "}},
		{"oversized subject", "oidc", ExternalIdentity{Subject: strings.Repeat("x", maxSubjectLen+1)}},
		{"claims assert role", "oidc", ExternalIdentity{Subject: "s", Claims: map[string]string{"role": "admin"}}},
		{"claims assert admin", "oidc", ExternalIdentity{Subject: "s", Claims: map[string]string{"IS_ADMIN": "1"}}},
		{"claims assert user_id", "oidc", ExternalIdentity{Subject: "s", Claims: map[string]string{"user_id": "..."}}},
		{"claims assert scopes", "oidc", ExternalIdentity{Subject: "s", Claims: map[string]string{"scopes": "admin"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.Resolve(context.Background(), tc.provider, tc.id)
			if !errors.Is(err, ErrExternalRejected) {
				t.Fatalf("got %v, want ErrExternalRejected", err)
			}
		})
	}
}

// The contract kit warns plugin authors about claims the host rejects. If the two lists drift,
// the kit either warns about something harmless (annoying) or — much worse — stays silent about a
// claim that will make every login fail in production. Pin them to each other.
func TestContractReservedClaimsMatchHost(t *testing.T) {
	for _, k := range contract.ReservedIdentityClaims {
		if _, ok := reservedClaimKeys[k]; !ok {
			t.Errorf("the contract kit warns about claim %q but the host does not reject it — authors would be "+
				"told to remove something harmless", k)
		}
	}
	kit := make(map[string]struct{}, len(contract.ReservedIdentityClaims))
	for _, k := range contract.ReservedIdentityClaims {
		kit[k] = struct{}{}
	}
	for k := range reservedClaimKeys {
		if _, ok := kit[k]; !ok {
			t.Errorf("the host rejects claim %q but the contract kit does not warn about it — an author would "+
				"ship it and every login would fail", k)
		}
	}
}
