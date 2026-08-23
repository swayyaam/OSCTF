// Package eventkeys is the SHARED definition of the event names core publishes and the Data keys
// each one carries — the contract a notification plugin depends on.
//
// It is a LEAF with no imports at all, and it lives outside internal/ on purpose: core builds every
// event's Data from it (internal/events) and the public SDK re-exports it (plugin/sdk.EventKeys),
// so the documented set cannot drift from what core actually emits. Putting it in internal/events
// instead would drag that package's database dependency into every plugin that wants to know an
// event's field names.
//
// All values are non-secret and non-PII (ids and slugs).
package eventkeys

// Event names published by core. A name here MUST have a Schema entry and a builder in
// internal/events.
const (
	EventChallengeSolved = "challenge.solved"
)

// Schema documents the Data keys each event type carries. Keys are sorted.
var Schema = map[string][]string{
	EventChallengeSolved: {"challenge_id", "challenge_slug", "team_id", "user_id"},
}
