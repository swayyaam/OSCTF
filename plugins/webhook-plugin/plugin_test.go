package main

import (
	"testing"

	"github.com/swayyaam/OSCTF/plugin/sdk"
	"github.com/swayyaam/OSCTF/plugin/sdk/contract"
)

// TestPluginContract builds this plugin and verifies it satisfies the OSCTF notification contract
// via the public harness. With no webhook URL configured the plugin accepts-and-drops, so the
// contract (the plugin accepts a subscribed event without failing the delivery) holds. Note the
// harness cannot verify the external side effect (the POST) — that is inherent to notifications.
func TestPluginContract(t *testing.T) {
	bin := contract.Build(t, ".")
	contract.VerifyNotification(t, bin, []contract.NotificationCase{
		{Name: "solved", Event: sdk.Event{Name: "challenge.solved", ID: "e1", Data: map[string]string{"team": "alpha", "challenge": "sanity"}}},
	})
}
