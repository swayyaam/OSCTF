package events_test

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/osctf/platform/internal/events"
	"github.com/osctf/platform/internal/metrics"
)

func matchAll(string) bool { return true }

func dropped(t *testing.T, name, event, reason string) float64 {
	t.Helper()
	return metrics.CounterValue(metrics.PluginEventsDropped.WithLabelValues(name, event, reason))
}

// Happy path: a subscriber receives matching events (and only matching ones), in order.
func TestBusDeliversMatchingEvents(t *testing.T) {
	bus := events.NewBus()
	var mu sync.Mutex
	var got []string
	done := make(chan struct{}, 8)
	cancel := bus.Subscribe("np-deliver", func(n string) bool { return n == "challenge.solved" },
		func(_ context.Context, e events.Event) error {
			mu.Lock()
			got = append(got, e.ID)
			mu.Unlock()
			done <- struct{}{}
			return nil
		})
	defer cancel()

	bus.Publish(events.Event{Name: "challenge.solved", ID: "a"})
	bus.Publish(events.Event{Name: "team.created", ID: "ignored"}) // not subscribed → not delivered
	bus.Publish(events.Event{Name: "challenge.solved", ID: "b"})

	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for delivery")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("delivered %v, want [a b] in order (team.created must not be delivered)", got)
	}
}

// REQUIREMENT 1: Publish must never block, under EVERY condition — including every subscriber queue
// full and a handler stuck mid-delivery. It runs after a tx commit on the hot write path, so a
// blocking Publish would re-create lock-across-I/O there. Fill every queue, then Publish from a
// goroutine with a deadline and confirm it returns immediately.
func TestPublishNeverBlocksWithFullQueues(t *testing.T) {
	bus := events.NewBus().WithQueueCap(1)
	hold := make(chan struct{})
	var cancels []func()
	for i := 0; i < 4; i++ {
		c := bus.Subscribe("np-block-"+strconv.Itoa(i), matchAll, func(ctx context.Context, _ events.Event) error {
			select {
			case <-hold: // block every delivery so queues fill and stay full
			case <-ctx.Done():
			}
			return nil
		})
		cancels = append(cancels, c)
	}
	defer func() {
		close(hold)
		for _, c := range cancels {
			c()
		}
	}()

	// Saturate: each subscriber's consumer takes one (blocked in the handler) and buffers one; the
	// rest drop. After this every queue is full.
	for i := 0; i < 50; i++ {
		bus.Publish(events.Event{Name: "x", ID: strconv.Itoa(i)})
	}

	returned := make(chan struct{})
	go func() {
		bus.Publish(events.Event{Name: "x", ID: "final"})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked with every subscriber queue full — it must never block on the hot write path")
	}
}

// Backpressure drops are counted (never silent), tagged reason=backpressure.
func TestBackpressureDropsCounted(t *testing.T) {
	bus := events.NewBus().WithQueueCap(1)
	hold := make(chan struct{})
	cancel := bus.Subscribe("np-bp", matchAll, func(ctx context.Context, _ events.Event) error {
		select {
		case <-hold:
		case <-ctx.Done():
		}
		return nil
	})
	defer func() { close(hold); cancel() }()

	before := dropped(t, "np-bp", "x", "backpressure")
	for i := 0; i < 20; i++ {
		bus.Publish(events.Event{Name: "x", ID: strconv.Itoa(i)})
	}
	if got := dropped(t, "np-bp", "x", "backpressure") - before; got <= 0 {
		t.Fatalf("backpressure drops counted = %v, want > 0 (a full queue must count every dropped event)", got)
	}
}

// DROP-NEWEST: with the consumer blocked during a burst, the queue keeps the FIRST cap+1 events and
// drops the tail — the delivered stream is a contiguous PREFIX, never holes in the middle. A
// drop-oldest bus would evict accepted events and deliver a suffix; this asserts we do not.
func TestDropNewestKeepsPrefixNotHoles(t *testing.T) {
	const cap = 4
	bus := events.NewBus().WithQueueCap(cap)
	var mu sync.Mutex
	var got []int
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	cancel := bus.Subscribe("np-prefix", matchAll, func(_ context.Context, e events.Event) error {
		once.Do(func() { close(started); <-release }) // block ONLY the first delivery, during the burst
		n, _ := strconv.Atoi(e.Data["n"])
		mu.Lock()
		got = append(got, n)
		mu.Unlock()
		return nil
	})
	defer cancel()

	bus.Publish(events.Event{Name: "x", ID: "1", Data: map[string]string{"n": "1"}})
	<-started // the consumer is now blocked in the handler on event 1
	for n := 2; n <= 20; n++ {
		bus.Publish(events.Event{Name: "x", ID: strconv.Itoa(n), Data: map[string]string{"n": strconv.Itoa(n)}})
	}
	close(release) // handler(1) returns; consumer drains 2..(cap+1), the rest were dropped

	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= cap+1 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("only %d events delivered, want %d", n, cap+1)
		case <-time.After(5 * time.Millisecond):
		}
	}
	time.Sleep(50 * time.Millisecond) // settle: ensure nothing extra arrives
	mu.Lock()
	defer mu.Unlock()
	want := []int{1, 2, 3, 4, 5} // the first cap+1 = a contiguous prefix, no event > 5
	if len(got) != len(want) {
		t.Fatalf("delivered %v, want a contiguous prefix %v (drop-newest)", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("delivered %v, want %v — a hole or a suffix means it is NOT drop-newest", got, want)
		}
	}
}

// A delivery that ERRORS is counted (reason=delivery), never silent.
func TestDeliveryErrorCounted(t *testing.T) {
	bus := events.NewBus()
	done := make(chan struct{}, 1)
	cancel := bus.Subscribe("np-err", matchAll, func(_ context.Context, _ events.Event) error {
		done <- struct{}{}
		return context.DeadlineExceeded
	})
	defer cancel()

	before := dropped(t, "np-err", "x", "delivery")
	bus.Publish(events.Event{Name: "x", ID: "1"})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler not called")
	}
	// the counter increments after the handler returns; poll briefly.
	deadline := time.After(time.Second)
	for dropped(t, "np-err", "x", "delivery")-before < 1 {
		select {
		case <-deadline:
			t.Fatal("delivery error not counted")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// REQUIREMENT 2 (mid-teardown): unsubscribing a subscriber with queued events discards them as
// COUNTED shutdown drops (a deliberate policy — the plugin is terminal, delivering its backlog is
// pointless), and the cancel returns promptly rather than hanging.
func TestUnsubscribeDropsQueuedAsShutdownCounted(t *testing.T) {
	bus := events.NewBus().WithQueueCap(8)
	entered := make(chan struct{}, 1)
	var once sync.Once
	cancel := bus.Subscribe("np-teardown", matchAll, func(ctx context.Context, _ events.Event) error {
		once.Do(func() { entered <- struct{}{} })
		<-ctx.Done() // block until teardown cancels the delivery ctx (keeps the queue undrained)
		return ctx.Err()
	})

	// Enqueue a batch: one is taken by the consumer (blocked in the handler on ctx), the rest sit
	// queued and undrained until teardown.
	for i := 0; i < 6; i++ {
		bus.Publish(events.Event{Name: "x", ID: strconv.Itoa(i)})
	}
	<-entered // consumer is blocked in the handler; the remaining events are queued
	before := dropped(t, "np-teardown", "x", "shutdown")

	// cancel cancels the in-flight delivery ctx (so the blocked handler returns) and drains the
	// queued events as counted shutdown drops — and it must return promptly, not hang a timeout.
	returned := make(chan struct{})
	go func() { cancel(); close(returned) }()
	select {
	case <-returned:
	case <-time.After(3 * time.Second):
		t.Fatal("cancel hung — unsubscribe must return promptly (teardown cancels the in-flight delivery)")
	}
	if got := dropped(t, "np-teardown", "x", "shutdown") - before; got <= 0 {
		t.Fatalf("shutdown drops counted = %v, want > 0 (queued events on teardown must be counted, not silent)", got)
	}
}
