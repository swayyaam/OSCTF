package sdk

import (
	"context"

	"github.com/osctf/platform/internal/plugin/pluginpb"
)

// FlagCheck is the input to a flag check. Submitted is what the player sent; Config is the
// challenge's per-challenge type_config (author-defined, validated + normalized at author time by
// ValidateConfig). The real static flag is NEVER sent to a plugin — a challenge-type plugin decides
// correctness from Config (and, once wired, Instance), not by being handed the answer.
//
// Instance is RESERVED and ALWAYS EMPTY today — do not read it. It is meant to carry per-team
// instance context (e.g. a per-instance secret) for a containerised per_instance challenge, but the
// host currently passes an empty map: a challenge that is BOTH per_instance and plugin-typed is not
// a wired combination yet (the built-in per_instance path handles per-team flags today). It will be
// populated from the instances table when that combination is supported, so treat it as absent, not
// merely unset — the same reserved-until-wired shape as sdk.Score.Params.
//
// Config MAY CONTAIN CHALLENGE-SENSITIVE DATA — a regex or rule that reveals the flag's structure is
// the obvious case. NEVER log it, or any value derived from it: sdk.Log output reaches the host log,
// and a flag hint leaked there defeats the challenge.
type FlagCheck struct {
	Submitted string
	Config    map[string]string
	Instance  map[string]string
}

// ConfigValidation is the result of author-time config validation. OK reports whether the config
// is usable; FieldErrors maps a config key to a human message when not; Normalized is the config
// the host should store in place of the raw input (defaults filled, values canonicalised).
type ConfigValidation struct {
	OK          bool
	FieldErrors map[string]string
	Normalized  map[string]string
}

// Checker is implemented by a challenge-type plugin.
type Checker interface {
	Info() Info
	// ValidateConfig runs at author time (when a challenge is created/edited), not on the submit
	// path — reject bad config before an event, not during one. Return OK=false with per-field
	// messages to reject the save (the admin sees a 422 with those fields); return Normalized to
	// canonicalise what the host stores. The config is per-challenge author input and MAY CONTAIN
	// CHALLENGE-SENSITIVE DATA (see FlagCheck.Config) — never log it.
	ValidateConfig(config map[string]string) ConfigValidation
	// CheckFlag decides whether a submission is correct. Returning an error means "could not
	// decide" (the host fails the check closed — the attempt is not consumed), which is
	// different from returning (false, nil), a decided-incorrect.
	CheckFlag(FlagCheck) (correct bool, err error)
}

// challengeTypeAdapter translates the generated gRPC ChallengeTypeServer into calls on the
// author's Checker.
type challengeTypeAdapter struct {
	pluginpb.UnimplementedChallengeTypeServer
	impl Checker
}

func (a *challengeTypeAdapter) Info(context.Context, *pluginpb.InfoRequest) (*pluginpb.InfoResponse, error) {
	return infoResponse(a.impl.Info(), pluginpb.PluginType_PLUGIN_TYPE_CHALLENGE_TYPE, "check"), nil
}

func (a *challengeTypeAdapter) ValidateConfig(_ context.Context, req *pluginpb.ValidateRequest) (*pluginpb.ValidateResponse, error) {
	r := a.impl.ValidateConfig(req.GetConfig())
	return &pluginpb.ValidateResponse{
		Ok:          r.OK,
		FieldErrors: r.FieldErrors,
		Normalized:  r.Normalized,
	}, nil
}

func (a *challengeTypeAdapter) CheckFlag(_ context.Context, req *pluginpb.CheckRequest) (*pluginpb.CheckResponse, error) {
	correct, err := a.impl.CheckFlag(FlagCheck{
		Submitted: req.GetSubmitted(),
		Config:    req.GetConfig(),
		Instance:  req.GetInstance(),
	})
	if err != nil {
		return nil, err
	}
	return &pluginpb.CheckResponse{Correct: correct}, nil
}
