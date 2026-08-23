// Package plugin is the host side of the OSCTF plugin ABI: discovery, the manifest, launching and
// supervising plugin processes, the in-flight budget, and the registry wiring. Plugins run as
// separate processes; the host dials a local gRPC server each serves.
//
// The ABI surface itself — handshake, version, dispense keys, transport bridge — lives in the
// PUBLIC package plugin/abi, because plugin authors need it too and must not have to link this
// package (and its server-side dependencies) to get it. The identifiers are re-exported here so
// host code refers to them unqualified, exactly as before.
//
// See docs/v0.3/02-plugin-abi.md and 03-plugin-loader.md.
package plugin

import (
	goplugin "github.com/hashicorp/go-plugin"

	"github.com/swayyaam/OSCTF/plugin/abi"
)

const (
	ABIMajor        = abi.ABIMajor
	ABIMinor        = abi.ABIMinor
	ABIString       = abi.ABIString
	PluginConfigEnv = abi.PluginConfigEnv

	KeyAuth          = abi.KeyAuth
	KeyScoring       = abi.KeyScoring
	KeyNotification  = abi.KeyNotification
	KeyChallengeType = abi.KeyChallengeType
)

// Handshake gates every plugin connection (see abi.Handshake).
var Handshake = abi.Handshake

type (
	AuthGRPCPlugin          = abi.AuthGRPCPlugin
	ScoringGRPCPlugin       = abi.ScoringGRPCPlugin
	NotificationGRPCPlugin  = abi.NotificationGRPCPlugin
	ChallengeTypeGRPCPlugin = abi.ChallengeTypeGRPCPlugin
)

// HostPluginSet is the set the host offers when dialing a plugin (see abi.HostPluginSet).
func HostPluginSet() goplugin.PluginSet { return abi.HostPluginSet() }
