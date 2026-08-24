// A webhook notification plugin built from the OSCTF template: it POSTs each subscribed event to an
// HTTP endpoint as JSON. Built entirely against the public plugin/sdk — no platform source.
//
// Rebuilt against the config + logging SDK: the URL comes from sdk.Config() (no invented env var),
// delivery is fire-and-forget with failures reported via sdk.Log() (no blocking the delivery path
// to surface an error). No OSCTF import but plugin/sdk.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/swayyaam/OSCTF/plugin/sdk"
)

type webhook struct {
	url    string
	client *http.Client
}

func (webhook) Info() sdk.Info          { return sdk.Info{Name: "webhook", Version: "0.1.0"} }
func (webhook) Subscriptions() []string { return []string{"*"} } // filter by returning specific event names

// Notify returns immediately and delivers on its own goroutine — the host does not block a solve on
// a webhook, and a delivery failure is reported via sdk.Log (host-captured) rather than by blocking.
func (w webhook) Notify(e sdk.Event) error {
	if w.url == "" {
		return nil // not configured: accept-and-drop
	}
	go w.post(e)
	return nil
}

func (w webhook) post(e sdk.Event) {
	body, err := json.Marshal(map[string]any{"event": e.Name, "id": e.ID, "occurred_at": e.OccurredAt, "data": e.Data})
	if err != nil {
		sdk.Log().Error("webhook: encode event failed", "event", e.Name, "error", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		sdk.Log().Error("webhook: build request failed", "error", err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		sdk.Log().Error("webhook delivery failed", "event", e.Name, "error", err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		sdk.Log().Warn("webhook returned non-2xx", "event", e.Name, "status", resp.StatusCode)
	}
}

func main() {
	// The URL comes from the manifest's webhook_url config (a secret; resolved from the environment
	// by the host and delivered via sdk.Config) — no invented env var, no reach into core.
	sdk.Serve(sdk.Notification, webhook{url: sdk.Config().String("webhook_url"), client: &http.Client{}})
}
