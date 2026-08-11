package main

import (
	"context"
	"sync"

	"github.com/osctf/platform/internal/challenges"
	"github.com/osctf/platform/internal/plugin"
	"github.com/osctf/platform/internal/plugin/pluginpb"
	"github.com/osctf/platform/internal/scoring"
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

// scoringPlugins holds the live scoring-plugin callers by mode, shared by the registrar (writer, on
// ready/revert) and the pluginScorer (reader, on the write path). A registered mode is ALSO added
// to scoring.Registry as a marker so challenge write-time validation (scoring.IsRegisteredMode)
// accepts it — but that marker is never consulted on the scoreboard read path (the board reads the
// recorded value), so the read path stays structurally free of any plugin call.
type scoringPlugins struct {
	mu      sync.RWMutex
	callers map[string]plugin.Caller
}

func newScoringPlugins() *scoringPlugins { return &scoringPlugins{callers: map[string]plugin.Caller{}} }

func (s *scoringPlugins) add(mode string, c plugin.Caller) {
	s.mu.Lock()
	s.callers[mode] = c
	s.mu.Unlock()
}

func (s *scoringPlugins) remove(mode string) {
	s.mu.Lock()
	delete(s.callers, mode)
	s.mu.Unlock()
}

func (s *scoringPlugins) caller(mode string) (plugin.Caller, bool) {
	s.mu.RLock()
	c, ok := s.callers[mode]
	s.mu.RUnlock()
	return c, ok
}

// scoringMarker is registered in scoring.Registry for a plugin scoring mode SOLELY so challenge
// write-time validation accepts the mode. It is a static fail-safe: the read path never calls it
// (compute reads recorded values for plugin modes), and if it ever were called it returns the
// static value rather than reaching the plugin — so the read path can never touch a plugin.
type scoringMarker struct{ mode string }

func (m scoringMarker) Name() string { return m.mode }
func (scoringMarker) Value(p scoring.ChallengeScoring, solves int) int {
	return scoring.StaticEngine{}.Value(p, solves)
}

// pluginScorer is the submission service's write-path Scorer (#9). It dispatches the plugin's Value
// RPC through the loader's Caller (readiness gate + in-flight budget), and applies the deployment's
// fallback policy when the plugin is down/deregistered or errs: fallback-on records the static
// value labelled 'fallback'; fallback-off records 'pending' for the repair worker to retry. It is
// NEVER consulted on the scoreboard read path.
type pluginScorer struct {
	plugins  *scoringPlugins
	fallback bool
}

func (p pluginScorer) Score(ctx context.Context, mode string, params scoring.ChallengeScoring, solves int) (int, string, bool) {
	if c, ok := p.plugins.caller(mode); ok {
		var value int
		err := c.Call(ctx, "Value", func(ctx context.Context, client any) error {
			//nolint:gosec // G115: Initial/Min/Decay round-trip from the challenge's int32 point
			// columns; Solves is a bounded solve count. None can overflow int32.
			resp, e := client.(pluginpb.ScoringClient).Value(ctx, &pluginpb.ScoreRequest{
				Initial: int32(params.Initial), Min: int32(params.Min), Decay: int32(params.Decay), Solves: int32(solves),
			})
			if e != nil {
				return e
			}
			value = int(resp.GetValue())
			return nil
		})
		if err == nil {
			return value, mode, true
		}
	}
	// Plugin down / deregistered / call error: apply the deployment's fallback policy.
	if p.fallback {
		return scoring.StaticEngine{}.Value(params, solves), "fallback", true
	}
	return 0, "pending", false
}

// pluginRegistrar wires ready plugins into their type registries and reverts them before death.
// challenge-type (#8) and scoring (#9) are wired here; notification (#10) and auth follow the same
// shape.
type pluginRegistrar struct {
	challengeTypes *challenges.TypeRegistry
	scoring        *scoringPlugins
}

func (r pluginRegistrar) Register(name, ptype string, c plugin.Caller) error {
	switch ptype {
	case "challenge_type":
		return r.challengeTypes.Register(name, challengeTypeAdapter{id: name, caller: c}, false)
	case "scoring":
		// Marker in the registry (for write-time validation), then the caller for the write path.
		if err := scoring.Register(name, scoringMarker{mode: name}, false); err != nil {
			return err // e.g. a plugin trying to shadow a protected built-in (static/dynamic)
		}
		r.scoring.add(name, c)
	}
	return nil // notification (#10) / auth land with their phases
}

func (r pluginRegistrar) Deregister(name, ptype string) {
	switch ptype {
	case "challenge_type":
		r.challengeTypes.Deregister(name)
	case "scoring":
		r.scoring.remove(name)
		scoring.Deregister(name)
	}
}
