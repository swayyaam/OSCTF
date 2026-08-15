// Command examplenotifier is a minimal notification plugin for the contract-harness test — the
// smallest honest example of the notification surface: subscribe, then handle an event. A real
// notifier would post to a webhook on its own goroutine; here Notify just accepts.
package main

import "github.com/osctf/platform/plugin/sdk"

type notifier struct{}

func (notifier) Info() sdk.Info          { return sdk.Info{Name: "example-webhook", Version: "0.1.0"} }
func (notifier) Subscriptions() []string { return []string{"challenge.solved"} }
func (notifier) Notify(sdk.Event) error  { return nil }

func main() { sdk.Serve(sdk.Notification, notifier{}) }
