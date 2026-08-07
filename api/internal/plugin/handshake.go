// Package plugin is the host side of the OSCTF plugin ABI: the go-plugin handshake, the gRPC
// transport bridge to the generated stubs (pluginpb), and (in later sub-steps) the loader,
// lifecycle, and registry wiring. Plugins run as separate processes; the host dials a local
// gRPC server each serves. See docs/v0.3/02-plugin-abi.md and 03-plugin-loader.md.
package plugin

import (
	goplugin "github.com/hashicorp/go-plugin"
)

// ABIMajor is the go-plugin ProtocolVersion — the ABI MAJOR, bumped ONLY on a breaking
// change. A plugin built against a different major is refused by go-plugin before any call;
// the loader logs "ABI major mismatch" and skips it (no crash, no partial init).
const ABIMajor = 1

// ABIMinor is the host's ABI minor. Minor is forward-compatible: the host may call a plugin
// advertising an OLDER minor (it won't invoke methods/fields the plugin lacks), and a plugin
// advertising a NEWER minor is accepted (the host uses only what it knows). Carried per-plugin
// in the manifest and the Info RPC.
const ABIMinor = 0

// ABIString is the host's advertised "major.minor".
const ABIString = "1.0"

// Handshake gates every plugin connection. The magic cookie guards against launching a
// non-OSCTF binary; ProtocolVersion is the ABI major (a mismatch is refused pre-call).
var Handshake = goplugin.HandshakeConfig{
	ProtocolVersion:  ABIMajor,
	MagicCookieKey:   "OSCTF_PLUGIN",
	MagicCookieValue: "osctf-plugin-v1",
}
