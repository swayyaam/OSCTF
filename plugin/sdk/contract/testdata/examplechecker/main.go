// Command examplechecker is a minimal challenge-type plugin used by the contract-harness test — the
// smallest honest example of the challenge-type author surface: implement sdk.Checker (Info,
// ValidateConfig, CheckFlag), call sdk.Serve. Nothing else — no go-plugin, no gRPC, no protobuf.
//
// It requires an "answer" key (validated + trimmed at author time) and accepts a submission equal to
// it. Note the two places it returns an ERROR rather than false: the host fails CLOSED on an error
// and consumes no attempt, so an error is how a checker says "I cannot decide" without costing the
// player a try. Returning false there would silently burn an attempt — the most consequential thing
// a checker author can get wrong.
package main

import (
	"errors"
	"strings"

	"github.com/swayyaam/OSCTF/plugin/sdk"
)

type checker struct{}

func (checker) Info() sdk.Info { return sdk.Info{Name: "example-keyword", Version: "0.1.0"} }

// ValidateConfig runs at author time: "answer" is required, and the STORED value is the trimmed form
// (what the host later hands back to CheckFlag).
func (checker) ValidateConfig(cfg map[string]string) sdk.ConfigValidation {
	answer := strings.TrimSpace(cfg["answer"])
	if answer == "" {
		return sdk.ConfigValidation{OK: false, FieldErrors: map[string]string{"answer": "is required"}}
	}
	return sdk.ConfigValidation{OK: true, Normalized: map[string]string{"answer": answer}}
}

// CheckFlag decides correctness from the submitted guess + the per-challenge config — NEVER the flag.
func (checker) CheckFlag(in sdk.FlagCheck) (bool, error) {
	answer, ok := in.Config["answer"]
	if !ok || answer == "" {
		// No usable config: the checker can't decide. Error (fail closed, no attempt burned), not a
		// false that would cost the player a try for the author's config mistake.
		return false, errors.New("no answer configured — cannot decide")
	}
	if in.Submitted == "" {
		// A blank submission carries nothing to check — error rather than counting it a wrong guess.
		return false, errors.New("empty submission — nothing to check")
	}
	return in.Submitted == answer, nil
}

func main() { sdk.Serve(sdk.ChallengeType, checker{}) }
