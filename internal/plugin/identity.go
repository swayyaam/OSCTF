package plugin

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"

	"github.com/osctf/platform/internal/plugin/pluginpb"
)

// identityTimeout bounds the single Info RPC the supervisor makes at ready to cross-check the
// running binary against its manifest. A well-behaved plugin answers Info instantly (it is a
// trivial RPC); a binary that cannot is either the wrong type or broken — either way a load fault.
const identityTimeout = 5 * time.Second

// infoClient is the Info RPC common to every generated plugin client (Scoring/Notification/
// ChallengeType/Auth all expose the identical Info method). The supervisor type-asserts the
// dispensed client to it so it can dial Info without knowing the concrete plugin type; a client
// that does not satisfy it (a fake launcher in tests) is simply not cross-checked.
type infoClient interface {
	Info(ctx context.Context, in *pluginpb.InfoRequest, opts ...grpc.CallOption) (*pluginpb.InfoResponse, error)
}

// pluginTypeName maps the wire PluginType enum back to the manifest `type` string, so a plugin's
// self-reported type can be compared against what its manifest declared.
func pluginTypeName(t pluginpb.PluginType) string {
	switch t {
	case pluginpb.PluginType_PLUGIN_TYPE_SCORING:
		return "scoring"
	case pluginpb.PluginType_PLUGIN_TYPE_NOTIFICATION:
		return "notification"
	case pluginpb.PluginType_PLUGIN_TYPE_CHALLENGE_TYPE:
		return "challenge_type"
	case pluginpb.PluginType_PLUGIN_TYPE_AUTH:
		return "auth"
	default:
		return ""
	}
}

// abiMajorOf parses the major from a "major.minor" ABI string, or -1 if it does not parse.
func abiMajorOf(abi string) int {
	major := abi
	if i := strings.IndexByte(abi, '.'); i >= 0 {
		major = abi[:i]
	}
	n, err := strconv.Atoi(strings.TrimSpace(major))
	if err != nil {
		return -1
	}
	return n
}

// verifyIdentity dials the plugin's Info RPC once, on the freshly dispensed client, to confirm the
// running binary matches what its manifest claims. This is the ONE consumer of the Info RPC — its
// whole reason to exist. Without it, a binary whose served type differs from its manifest launches,
// registers, and fails every real call with Unimplemented: a scoring plugin only at the FIRST
// SOLVE, mid-event, because nothing calls it until then (the handshake checks only the ABI major,
// and Dispense builds a client stub without round-tripping). Dialing Info at ready surfaces the
// mismatch at LOAD instead — a wrong-type binary cannot answer Info on the manifest-typed service
// (the dispensed client hits Unimplemented), and a right-type binary answers, whereupon its
// name/type/ABI are checked too. Returns "" when consistent, else a human reason for the
// quarantine. A conn whose client is not an infoClient (a fake launcher in tests) is not checked.
func (s *supervisor) verifyIdentity(c *conn) string {
	if s.expectType == "" {
		return "" // no declared type to check against — a test-harness supervisor; production always sets it (manifest.validate guarantees a non-empty type)
	}
	ic, ok := c.client.(infoClient)
	if !ok {
		return "" // a fake launcher's client (tests) has no Info RPC — nothing to cross-check
	}
	ctx, cancel := context.WithTimeout(context.Background(), identityTimeout)
	defer cancel()
	resp, err := ic.Info(ctx, &pluginpb.InfoRequest{})
	if err != nil {
		return fmt.Sprintf("the binary did not answer Info on the %q service its manifest declares (%v) — "+
			"it likely serves a different plugin type than the manifest", s.expectType, err)
	}
	if got := pluginTypeName(resp.GetType()); got != s.expectType {
		return fmt.Sprintf("manifest declares type %q but the binary reports type %q", s.expectType, got)
	}
	if got := resp.GetName(); got != s.name {
		return fmt.Sprintf("manifest name %q but the binary reports name %q — a mispackaged manifest/binary pair", s.name, got)
	}
	if got := abiMajorOf(resp.GetAbi()); got != ABIMajor {
		return fmt.Sprintf("the binary was built against ABI major %d, host ABI major is %d", got, ABIMajor)
	}
	return ""
}

// quarantineIdentity parks a plugin that failed the identity cross-check in `failed`, with an
// admin-visible reason and the load-failed metric. An identity mismatch is a PERMANENT fault (the
// binary is wrong, not flaky), so it skips the crash-loop backoff: it quarantines at once and parks
// until stop or an operator reload. Returns true if the actor was stopped while parked.
func (s *supervisor) quarantineIdentity(ctx context.Context, reason string) (stop bool) {
	s.l.setLoadReason(s.name, reason)
	s.l.revert(s.name) // not yet registered, but idempotent
	s.quarantine()     // launching → failed (direct, legal)
	s.cfg.log.Error("plugin quarantined: identity mismatch with manifest", "plugin", s.name, "reason", reason)
	return s.parkUntilReload(ctx)
}
