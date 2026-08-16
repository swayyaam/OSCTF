package events

import (
	"reflect"
	"testing"
)

// The documented Schema must match what the builders actually produce, in BOTH directions: a key a
// builder emits that Schema omits fails, and a Schema entry with no builder-check fails. This is
// what keeps the plugin-facing event docs from drifting from what core emits — plugins will depend
// on these keys the moment the first reference plugin ships.
func TestSchemaMatchesBuilders(t *testing.T) {
	// One entry per event: the event name → a builder invocation with placeholder values.
	builders := map[string]map[string]string{
		EventChallengeSolved: SolvedData("t", "u", "c", "s"),
	}

	for name, data := range builders {
		want, ok := Schema[name]
		if !ok {
			t.Errorf("event %q has a builder but no Schema entry", name)
			continue
		}
		if got := SortedKeys(data); !reflect.DeepEqual(got, want) {
			t.Errorf("event %q: builder emits keys %v, Schema documents %v — reconcile them", name, got, want)
		}
	}

	if len(builders) != len(Schema) {
		t.Errorf("Schema has %d event(s) but only %d are pinned by a builder-check — add the missing case(s)",
			len(Schema), len(builders))
	}
}
