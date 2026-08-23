package events

import (
	"sort"

	"github.com/swayyaam/OSCTF/plugin/eventkeys"
)

// Event names published by core. A name here MUST have a Schema entry and a builder below.
const (
	EventChallengeSolved = eventkeys.EventChallengeSolved
)

// Schema documents the Data keys each event type carries — the contract a notification plugin
// depends on. It is the ONE source of truth, defined in the shared leaf plugin/eventkeys so the
// public SDK re-exports the same map core builds from. TestSchemaMatchesBuilders pins each builder
// against its entry.
var Schema = eventkeys.Schema

// SolvedData builds the Data for a challenge.solved event. Core uses this, so the emitted keys are
// exactly Schema[EventChallengeSolved] by construction — add a key here and TestSchemaMatchesBuilders
// fails until Schema (and thus the plugin-facing docs) is updated too.
func SolvedData(teamID, userID, challengeID, challengeSlug string) map[string]string {
	return map[string]string{
		"team_id":        teamID,
		"user_id":        userID,
		"challenge_id":   challengeID,
		"challenge_slug": challengeSlug,
	}
}

// SortedKeys returns the sorted keys of m (for schema comparison).
func SortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
