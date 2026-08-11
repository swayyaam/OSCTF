package main

import (
	"context"

	"github.com/osctf/platform/internal/challenges"
	"github.com/osctf/platform/internal/plugin"
	"github.com/osctf/platform/internal/plugin/pluginpb"
	"github.com/osctf/platform/internal/submissions"
)

// challengeTypeResolver adapts the challenge-type registry to the submission path's
// ChallengeTypes interface (so submissions does not import challenges). A type that resolves to a
// plugin checker (implements CheckFlag) takes the pre-tx verdict path; a built-in resolves as
// registered-but-not-plugin; an unresolved type is a plugin whose plugin is down/unknown.
type challengeTypeResolver struct{ reg *challenges.TypeRegistry }

func (r challengeTypeResolver) Resolve(id string) (submissions.FlagChecker, bool, bool) {
	ct, ok := r.reg.Get(id)
	if !ok {
		return nil, false, false // unregistered → reject-retry
	}
	if fc, isPlugin := ct.(submissions.FlagChecker); isPlugin {
		return fc, true, true
	}
	return nil, false, true // built-in → in-tx comparison
}

// challengeTypeAdapter is a plugin-backed challenge type: its CheckFlag dispatches to the plugin
// process through the loader's Caller (readiness gate + in-flight budget applied), and it never
// sends the flag. It satisfies both challenges.ChallengeType (for the registry) and
// submissions.FlagChecker (for the resolver) via the one CheckFlag method.
type challengeTypeAdapter struct {
	id     string
	caller plugin.Caller
}

func (a challengeTypeAdapter) ID() string                                         { return a.id }
func (challengeTypeAdapter) ValidateConfig(map[string]string) map[string][]string { return nil }

func (a challengeTypeAdapter) CheckFlag(ctx context.Context, submitted string, config, instance map[string]string) (bool, error) {
	var correct bool
	err := a.caller.Call(ctx, "CheckFlag", func(ctx context.Context, client any) error {
		resp, e := client.(pluginpb.ChallengeTypeClient).CheckFlag(ctx, &pluginpb.CheckRequest{
			Submitted: submitted, Config: config, Instance: instance,
		})
		if e != nil {
			return e
		}
		correct = resp.GetCorrect()
		return nil
	})
	return correct, err
}

// pluginRegistrar wires ready plugins into their type registries and reverts them before death.
// Only challenge-type is wired here (#8); scoring (#9) and notification (#10) land with their
// phases, and auth follows the same shape.
type pluginRegistrar struct {
	challengeTypes *challenges.TypeRegistry
}

func (r pluginRegistrar) Register(name, ptype string, c plugin.Caller) error {
	if ptype == "challenge_type" {
		return r.challengeTypes.Register(name, challengeTypeAdapter{id: name, caller: c}, false)
	}
	return nil // scoring (#9) / notification (#10) / auth land with their phases
}

func (r pluginRegistrar) Deregister(name, ptype string) {
	if ptype == "challenge_type" {
		r.challengeTypes.Deregister(name)
	}
}
