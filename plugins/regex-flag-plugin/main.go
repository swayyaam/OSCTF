// Command regex-flag is a reference OSCTF challenge-type plugin: it decides flag correctness by
// matching a submission against a per-challenge regular expression. The pattern lives in the
// challenge's `type_config` (author-defined, validated at author time) — NOT in per-deployment
// sdk.Config — so ONE instance of this plugin serves every regex challenge in the event, each with
// its own pattern. That per-challenge tuning is what makes a challenge-type plugin more than a
// single hardcoded challenge.
//
// The distinction this example exists to teach: CheckFlag returns an ERROR, not false, when it
// cannot decide. The host fails CLOSED on an error and consumes NO attempt; returning false instead
// silently burns the player's try. "The checker had a problem" and "your flag was wrong" are
// different answers — never collapse the first into the second, or a config or plugin fault costs
// players their limited attempts.
package main

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/swayyaam/OSCTF/plugin/sdk"
)

type checker struct{}

func (checker) Info() sdk.Info { return sdk.Info{Name: "regex-flag", Version: "0.1.0"} }

// ValidateConfig runs at AUTHOR time (challenge create/edit), never on the submit path. "pattern" is
// a required RE2 regular expression; a missing or uncompilable pattern is rejected with a per-field
// error the admin sees on save — so a broken regex fails when the challenge is written, not silently
// during the event. The stored (normalized) value is the pattern verbatim, which is exactly what
// CheckFlag is later handed.
func (checker) ValidateConfig(cfg map[string]string) sdk.ConfigValidation {
	pattern := cfg["pattern"]
	if pattern == "" {
		return sdk.ConfigValidation{OK: false, FieldErrors: map[string]string{"pattern": "is required"}}
	}
	if _, err := regexp.Compile(pattern); err != nil {
		// Go's regexp error is "error parsing regexp: <reason>"; strip the redundant prefix so the
		// admin reads one clear sentence, e.g. "is not a valid RE2 regular expression: missing
		// closing ): `OSCTF{(`", not "…regular expression: error parsing regexp: missing…".
		reason := strings.TrimPrefix(err.Error(), "error parsing regexp: ")
		return sdk.ConfigValidation{OK: false, FieldErrors: map[string]string{
			"pattern": "is not a valid RE2 regular expression: " + reason,
		}}
	}
	// Anchor your pattern (^…$) if a full-string match is what you mean — regexp matches a SUBSTRING
	// by default, so `OSCTF\{.+\}` would accept "junk OSCTF{x} junk". The plugin stores the pattern
	// as written; anchoring is the author's call, so it isn't forced here.
	return sdk.ConfigValidation{OK: true, Normalized: map[string]string{"pattern": pattern}}
}

// CheckFlag decides correctness by matching the submission against the per-challenge pattern. It
// never receives the flag itself — it decides from the pattern in `Config` and the submitted guess.
//
// It ERRORS, never returns false, when it CANNOT decide: an absent or (defensively) uncompilable
// stored pattern is the CHECKER failing, not the player being wrong. The host fails closed on the
// error and burns no attempt; a false there would cost the player a try for a fault they didn't
// cause. A submission that simply doesn't match is a decided FALSE — that is a wrong answer, and it
// correctly consumes an attempt.
func (checker) CheckFlag(in sdk.FlagCheck) (bool, error) {
	pattern, ok := in.Config["pattern"]
	if !ok || pattern == "" {
		return false, errors.New("no pattern configured — the checker cannot decide")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		// Validated at author time, so this is defensive. If it ever fires it is a checker fault, not
		// a wrong flag: return an error (fail closed), never false.
		return false, fmt.Errorf("stored pattern does not compile: %w", err)
	}
	return re.MatchString(in.Submitted), nil
}

func main() { sdk.Serve(sdk.ChallengeType, checker{}) }
