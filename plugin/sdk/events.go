package sdk

import "github.com/swayyaam/OSCTF/plugin/eventkeys"

// EventKeys returns the Data keys the given event type carries — the fields you can read from an
// sdk.Event.Data in a Notifier. For example EventKeys("challenge.solved") returns challenge_id,
// challenge_slug, team_id, user_id. Empty for an unknown event.
//
// These are the SAME keys core emits (one shared definition, pinned by a test), so they cannot
// drift from what your Notifier actually receives. All values are non-secret, non-PII.
func EventKeys(event string) []string {
	return append([]string(nil), eventkeys.Schema[event]...)
}
