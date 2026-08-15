package auth

import (
	"context"
	"strings"
	"testing"
)

func TestHashVerifyRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, pw := range []string{"password123", "correct horse battery staple", "🚩🚩🚩🚩🚩🚩🚩🚩", strings.Repeat("x", 128)} {
		encoded, err := HashPassword(ctx, pw)
		if err != nil {
			t.Fatalf("HashPassword(%q): %v", pw, err)
		}
		if !strings.HasPrefix(encoded, "$argon2id$v=19$m=65536,t=3,p=4$") {
			t.Errorf("PHC prefix wrong: %s", encoded)
		}
		ok, err := VerifyPassword(ctx, pw, encoded)
		if err != nil || !ok {
			t.Errorf("VerifyPassword(%q) = %v, %v; want true, nil", pw, ok, err)
		}
		ok, err = VerifyPassword(ctx, pw+"x", encoded)
		if err != nil || ok {
			t.Errorf("VerifyPassword(wrong) = %v, %v; want false, nil", ok, err)
		}
	}
}

func TestHashUniqueSalts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, _ := HashPassword(ctx, "same")
	b, _ := HashPassword(ctx, "same")
	if a == b {
		t.Fatal("two hashes of the same password are identical (salt reuse)")
	}
}

func TestVerifyRejectsGarbage(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"", "plaintext", "$argon2i$v=19$m=1,t=1,p=1$AA$AA", "$argon2id$v=18$m=1,t=1,p=1$AA$AA"} {
		if _, err := VerifyPassword(context.Background(), "x", bad); err == nil {
			t.Errorf("VerifyPassword accepted invalid hash %q", bad)
		}
	}
}

func TestNeedsRehash(t *testing.T) {
	t.Parallel()
	current, _ := HashPassword(context.Background(), "x")
	if NeedsRehash(current) {
		t.Error("NeedsRehash(current params) = true")
	}
	old := "$argon2id$v=19$m=32768,t=2,p=2$c29tZXNhbHQxMjM0NTY$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if !NeedsRehash(old) {
		t.Error("NeedsRehash(old params) = false")
	}
	if !NeedsRehash("garbage") {
		t.Error("NeedsRehash(garbage) = false")
	}
}

func TestBurnHashDoesNotPanic(t *testing.T) {
	t.Parallel()
	if err := BurnHash(context.Background(), "anything"); err != nil {
		t.Errorf("BurnHash returned unexpected error: %v", err)
	}
}
