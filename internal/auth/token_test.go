package auth

import (
	"strings"
	"testing"
)

func TestGenerateToken(t *testing.T) {
	p1, h1, pre1, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if !strings.HasPrefix(p1, TokenPrefix) {
		t.Errorf("plaintext %q lacks %q prefix", p1, TokenPrefix)
	}
	if len(h1) != 64 {
		t.Errorf("hash %q is not a 64-char sha-256 hex", h1)
	}
	if h1 != hashToken(p1) {
		t.Error("returned hash != hashToken(plaintext)")
	}
	body := strings.TrimPrefix(p1, TokenPrefix)
	if pre1 != body[:tokenPrefixLen] {
		t.Errorf("prefix %q != first %d chars of body", pre1, tokenPrefixLen)
	}

	// Two tokens must differ in plaintext, hash, and (with overwhelming probability) prefix.
	p2, h2, _, _ := GenerateToken()
	if p1 == p2 || h1 == h2 {
		t.Error("two generated tokens collided")
	}
}

func TestValidateScopes(t *testing.T) {
	if err := ValidateScopes([]string{ScopeRead, ScopeSubmit, ScopeAdmin}); err != nil {
		t.Errorf("known scopes rejected: %v", err)
	}
	if err := ValidateScopes(nil); err != nil {
		t.Errorf("empty scope set rejected: %v", err)
	}
	if err := ValidateScopes([]string{ScopeRead, "superuser"}); err == nil {
		t.Error("unknown scope accepted — must fail closed")
	}
}

func TestParsePresented(t *testing.T) {
	p, _, _, _ := GenerateToken()
	prefix, hash, ok := parsePresented(p)
	if !ok {
		t.Fatal("a valid token did not parse")
	}
	if hash != hashToken(p) {
		t.Error("parsed hash != hashToken(plaintext)")
	}
	body := strings.TrimPrefix(p, TokenPrefix)
	if prefix != body[:tokenPrefixLen] {
		t.Errorf("parsed prefix %q wrong", prefix)
	}

	for _, bad := range []string{"", "garbage", "Bearer x", TokenPrefix, TokenPrefix + "short"} {
		if _, _, ok := parsePresented(bad); ok {
			t.Errorf("parsed a non-token %q as valid", bad)
		}
	}
}
