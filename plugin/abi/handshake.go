// Package abi is the OSCTF plugin ABI surface SHARED by the host and by plugin authors: the
// go-plugin handshake, the ABI version, the dispense keys, and the gRPC transport bridge to the
// generated stubs (pluginpb).
//
// It is deliberately a LEAF: it imports go-plugin, grpc, and pluginpb, and nothing else. The host
// loader (internal/plugin) and the public SDK (plugin/sdk) both build on it, so a plugin author
// links the ABI without linking the loader — and therefore without inheriting the platform's
// server-side dependencies (the database driver, the metrics stack). Keep it that way: an import
// added here is an import every plugin in existence inherits.
//
// See docs/v0.3/02-plugin-abi.md and 03-plugin-loader.md.
package abi

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
