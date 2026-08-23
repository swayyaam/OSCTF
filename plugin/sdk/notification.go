package sdk

import (
	"context"

	"github.com/swayyaam/OSCTF/internal/plugin/pluginpb"
)

// Event is one thing that happened, delivered to a subscribed notification plugin. Data carries
// the event-specific fields (e.g. team, challenge for "challenge.solved"). Delivery is
// best-effort and fire-and-forget: the host does not block a solve on a notification, and a full
// queue drops the newest event (counted, never silent) — so a Notifier must not assume it sees
// every event, and must not do slow work inline.
type Event struct {
	Name       string            // e.g. "challenge.solved"
	ID         string            // unique event id (for dedupe if the author needs it)
	OccurredAt string            // RFC3339 timestamp, as a string on the wire
	Data       map[string]string // event-specific fields — the documented keys are EventKeys(Name)
}

// Notifier is implemented by a notification plugin.
type Notifier interface {
	Info() Info
	// Subscriptions returns the event names to receive; a single "*" means all events.
	Subscriptions() []string
	// Notify handles one delivered event. Returning an error signals the host the delivery
	// failed (it is logged/counted); returning nil means handled. It must be quick — do slow
	// or unreliable work (HTTP posts, etc.) on the plugin's own goroutine/queue, not inline.
	Notify(Event) error
}

// notificationAdapter translates the generated gRPC NotificationServer into calls on the author's
// Notifier.
type notificationAdapter struct {
	pluginpb.UnimplementedNotificationServer
	impl Notifier
}

func (a *notificationAdapter) Info(context.Context, *pluginpb.InfoRequest) (*pluginpb.InfoResponse, error) {
	return infoResponse(a.impl.Info(), pluginpb.PluginType_PLUGIN_TYPE_NOTIFICATION), nil
}

func (a *notificationAdapter) Subscriptions(context.Context, *pluginpb.InfoRequest) (*pluginpb.SubscriptionList, error) {
	return &pluginpb.SubscriptionList{EventNames: a.impl.Subscriptions()}, nil
}

func (a *notificationAdapter) Notify(_ context.Context, e *pluginpb.Event) (*pluginpb.NotifyAck, error) {
	err := a.impl.Notify(Event{
		Name:       e.GetName(),
		ID:         e.GetId(),
		OccurredAt: e.GetOccurredAt(),
		Data:       e.GetData(),
	})
	if err != nil {
		return nil, err
	}
	return &pluginpb.NotifyAck{Handled: true}, nil
}
