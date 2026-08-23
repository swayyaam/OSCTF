// Package sdk is the public, importable surface for writing an OSCTF plugin. An author
// implements one small Go interface per plugin type and calls Serve; the go-plugin handshake,
// the gRPC transport, and the generated wire types (pluginpb) are all hidden. This is the ONLY
// OSCTF package a plugin imports — never internal/*.
//
// The wire format is deliberately WRAPPED, not aliased. The whole point of versioning the ABI
// is that the wire can change under a stable author-facing surface: an alias would make the
// protobuf the author's API, so any wire change would break every plugin and the ABI major
// version would buy nothing. The translation (the line count) is what makes that real.
//
// The SDK owns the contract facts — the plugin TYPE (from the Serve argument), the ABI (a fact
// of the SDK build), and the CAPABILITIES (derived structurally from the interfaces an impl
// satisfies). An author declares none of them, so none can be misdeclared.
//
// See docs/v0.3/02-plugin-abi.md and docs/v0.3/11-plugin-template.md.
package sdk

import (
	goplugin "github.com/hashicorp/go-plugin"

	"github.com/swayyaam/OSCTF/internal/plugin"
	"github.com/swayyaam/OSCTF/internal/plugin/pluginpb"
)

// PluginType is the kind of provider a plugin implements. It matches the manifest `type`.
type PluginType string

// The four plugin types.
const (
	Scoring       PluginType = "scoring"
	Notification  PluginType = "notification"
	ChallengeType PluginType = "challenge_type"
	Auth          PluginType = "auth"
)

// Info is what a plugin advertises about itself: Name is the manifest name the host registers the
// provider under; Version is the plugin's own release. Type, ABI, and Capabilities are NOT here —
// the SDK owns them (see the package doc), so a plugin cannot misdeclare the contract it speaks.
type Info struct {
	Name    string
	Version string
}

// Serve runs the plugin over the OSCTF handshake and BLOCKS until the host disconnects (or the
// process is killed). It wires go-plugin + gRPC; the author touches neither. t must match impl —
// a Scoring plugin's impl must be a Scorer, an Auth plugin's a PasswordAuth and/or RedirectAuth,
// and so on — and a mismatch panics before serving, so it surfaces on the first run rather than
// as a silent no-op.
func Serve(t PluginType, impl any) {
	switch t {
	case Scoring:
		s, ok := impl.(Scorer)
		if !ok {
			panic("sdk.Serve(Scoring, …): impl must implement sdk.Scorer")
		}
		serve(plugin.KeyScoring, &plugin.ScoringGRPCPlugin{Impl: &scoringAdapter{impl: s}})
	case Notification:
		n, ok := impl.(Notifier)
		if !ok {
			panic("sdk.Serve(Notification, …): impl must implement sdk.Notifier")
		}
		serve(plugin.KeyNotification, &plugin.NotificationGRPCPlugin{Impl: &notificationAdapter{impl: n}})
	case ChallengeType:
		c, ok := impl.(Checker)
		if !ok {
			panic("sdk.Serve(ChallengeType, …): impl must implement sdk.Checker")
		}
		serve(plugin.KeyChallengeType, &plugin.ChallengeTypeGRPCPlugin{Impl: &challengeTypeAdapter{impl: c}})
	case Auth:
		serve(plugin.KeyAuth, &plugin.AuthGRPCPlugin{Impl: newAuthAdapter(impl)})
	default:
		panic("sdk.Serve: unknown plugin type " + string(t))
	}
}

// serve is the shared go-plugin entrypoint. The dispense key and the *GRPCPlugin bridge come from
// the type switch above, so no wire type ever appears in an exported signature.
func serve(key string, p goplugin.Plugin) {
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: plugin.Handshake,
		Plugins:         goplugin.PluginSet{key: p},
		GRPCServer:      goplugin.DefaultGRPCServer,
	})
}

// infoResponse composes the wire Info from the author's declaration plus the SDK-owned Type, ABI,
// and Capabilities. Author-supplied Type/ABI/Capabilities do not exist by construction.
func infoResponse(i Info, t pluginpb.PluginType, caps ...string) *pluginpb.InfoResponse {
	return &pluginpb.InfoResponse{
		Name:         i.Name,
		Type:         t,
		Abi:          plugin.ABIString,
		Version:      i.Version,
		Capabilities: caps,
	}
}
