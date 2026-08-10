package plugin

import "fmt"

// State is the lifecycle state of a tracked plugin. Every plugin is in exactly one; the
// full state machine is specified in docs/v0.3/03-plugin-loader.md before the loader is
// built, because process supervision is a new concurrency surface and the last two
// releases shipped bugs of exactly this shape.
type State string

const (
	StateDiscovered State = "discovered" // manifest found + validated; not launched
	StateLaunching  State = "launching"  // process started; handshake+Info+Configure in progress
	StateReady      State = "ready"      // launched, configured, registered, health passing — SERVES
	StateUnhealthy  State = "unhealthy"  // was ready; a health check or call failed; supervisor deciding
	StateRestarting State = "restarting" // being torn down + relaunched; backoff between attempts
	StateFailed     State = "failed"     // restart cap exhausted → quarantined; not retried automatically
	StateDraining   State = "draining"   // reload/shutdown; no new calls, in-flight allowed to finish
	StateStopped    State = "stopped"    // process exited, resources reclaimed, entry removed — terminal
)

// legalTransitions[from] is the set of states `from` may move to. Anything absent is a bug
// the state-machine test rejects. Mirrors the "Legal transitions" table in the spec.
var legalTransitions = map[State]map[State]struct{}{
	StateDiscovered: {StateLaunching: {}},
	StateLaunching:  {StateReady: {}, StateRestarting: {}, StateFailed: {}},
	StateReady:      {StateUnhealthy: {}, StateDraining: {}},
	StateUnhealthy:  {StateReady: {}, StateRestarting: {}, StateDraining: {}},
	StateRestarting: {StateLaunching: {}, StateFailed: {}},
	StateFailed:     {StateLaunching: {}}, // explicit reload is the ONLY exit from quarantine
	StateDraining:   {StateStopped: {}},
	StateStopped:    {}, // terminal
}

// machine is a plugin's state with guarded transitions. It is not safe for concurrent use
// on its own; the loader holds it under the per-plugin lock.
type machine struct {
	state State
}

// to attempts the transition and returns an error (leaving the state unchanged) if it is
// not one the spec permits.
func (m *machine) to(next State) error {
	if _, ok := legalTransitions[m.state][next]; !ok {
		return fmt.Errorf("plugin: illegal state transition %s -> %s", m.state, next)
	}
	m.state = next
	return nil
}

// servesNew reports whether a plugin in this state may be handed a NEW request. Only ready
// does; draining finishes in-flight calls but admits no new ones, so it is false here too.
func (m *machine) servesNew() bool { return m.state == StateReady }
