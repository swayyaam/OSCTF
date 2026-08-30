package main

import (
	"fmt"

	"github.com/zalando/go-keyring"
)

// Where a token lives.
//
// The OS keychain is preferred, and the config file is the documented fallback — written 0600,
// with `login` saying so out loud rather than quietly leaving a bearer credential in a dotfile.
// Being told is the difference between an informed trade-off and a surprise.
//
// The keychain is genuinely unavailable in normal situations — a headless CI runner, a Linux box
// with no D-Bus session — so the fallback is a supported path, not an error case.
const keyringService = "osctf"

// storeToken saves the token for a context and reports whether the keychain accepted it.
func storeToken(contextName, token string) (usedKeychain bool) {
	if err := keyring.Set(keyringService, contextName, token); err == nil {
		return true
	}
	return false
}

// readToken resolves a context's token from wherever login put it.
func readToken(contextName string, ctx Context) (string, error) {
	if ctx.KeychainRef {
		tok, err := keyring.Get(keyringService, contextName)
		if err != nil {
			return "", errf(exitAuth,
				"context %q keeps its token in the OS keychain, which is unreadable here (%v) — "+
					"re-run `osctf login`, or pass --token/OSCTF_TOKEN", contextName, err)
		}
		return tok, nil
	}
	if ctx.Token == "" {
		return "", errf(exitAuth, "context %q has no token — run `osctf login --context %s`", contextName, contextName)
	}
	return ctx.Token, nil
}

// deleteToken removes a context's stored credential from both possible homes. Errors are
// deliberately ignored: logout must succeed even if the keychain entry is already gone, and
// leaving a context behind because a delete failed would be worse than a redundant no-op.
func deleteToken(contextName string) {
	_ = keyring.Delete(keyringService, contextName)
}

func keychainNote(used bool) string {
	if used {
		return "Token stored in the OS keychain."
	}
	return fmt.Sprintf("The OS keychain was unavailable, so the token was written to the config "+
		"file with %#o permissions. Anyone who can read that file can act as you.", 0o600)
}
