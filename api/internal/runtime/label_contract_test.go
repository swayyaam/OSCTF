package runtime

import "testing"

// TestLabelContract pins the exact label keys reconcile adopts containers and
// networks by. Deploy APPLIES these keys and Reconcile / ListUnadopted* READ them via
// the SAME constants, so a container's row is found only if the applied key equals the
// read key. Renaming a constant's *value* would silently orphan every managed resource
// (nothing would match), so it must fail a test instead — here.
func TestLabelContract(t *testing.T) {
	for _, tc := range []struct {
		name, got, want string
	}{
		{"managed", managedLabel, "osctf.managed"},
		{"instance_id", instanceIDLabel, "osctf.instance_id"},
		{"challenge_id", challengeIDLabel, "osctf.challenge_id"},
		{"team_network", teamNetworkLabel, "osctf.team_network"},
		{"team_id", teamIDLabel, "osctf.team_id"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s label = %q, want %q — renaming it silently orphans resources", tc.name, tc.got, tc.want)
		}
	}
}
