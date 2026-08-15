package runtime_test

import (
	"testing"

	"go.uber.org/goleak"
)

// The dockerint tier talks to the daemon over the Docker client's net/http
// transport, which keeps a bounded pool of keepalive connections alive for the
// process (the client is not closed — it lives for the daemon session, like in
// production). Those persistConn read/write loops are library goroutines, not a
// leak in our logic, so they're ignored; any of OUR goroutines that outlive the
// tests still fail here.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreAnyFunction("net/http.(*persistConn).writeLoop"),
		goleak.IgnoreAnyFunction("net/http.(*persistConn).readLoop"),
	)
}
