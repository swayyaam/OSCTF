package events

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/swayyaam/OSCTF/internal/metrics"
)

// Event is a typed domain event published AFTER a transaction commits. Data is flat and carries
// NON-secret, NON-PII fields only (it may be handed to an external notification plugin).
type Event struct {
	Name       string            // "challenge.solved"
	ID         string            // unique id, for subscriber-side dedup
	OccurredAt time.Time         // when it happened (RFC3339 on the wire)
	Data       map[string]string // flat, non-secret
}

// Handler receives one event. It runs on the subscriber's own goroutine, bounded by the bus's
// delivery timeout; a non-nil error is counted as a dropped delivery.
type Handler func(ctx context.Context, e Event) error

// defaults for the bus knobs.
const (
	defaultQueueCap       = 64
	defaultDeliverTimeout = 5 * time.Second
)

// Bus is a best-effort, fire-and-forget publish/subscribe for domain events, distinct from the
// event-*window* Service in this package. Its contract:
//
//   - Publish NEVER blocks. It is called after a tx commit on the hot write path (a solve), so
//     anything that could block there would re-create the lock-across-I/O shape on the hottest
//     write path. Publish takes a lock-free snapshot of subscribers and does a non-blocking send to
//     each bounded queue — it cannot block on a full queue, a slow subscriber, or a subscriber
//     mid-teardown.
//   - Delivery is asynchronous, per-subscriber, IN ORDER, and DROP-NEWEST: when a subscriber's
//     bounded queue is full the INCOMING event is dropped, never an already-queued one. So the
//     delivered stream is a prefix of what happened — lossy at the tail, never a hole in the middle
//     (drop-oldest would evict an accepted event, so a subscriber would see events 1 and 3 with no
//     signal that 2 existed: lag vs corruption for anything sequence-sensitive).
//   - It FAILS OPEN and is never silent. A drop never gates the action that published the event;
//     every drop is COUNTED (metrics.PluginEventsDropped) by subscriber, event, and reason.
type Bus struct {
	subs           atomic.Pointer[[]*subscriber] // lock-free read on the Publish path
	writeMu        sync.Mutex                    // serializes Subscribe/remove (copy-on-write)
	queueCap       int
	deliverTimeout time.Duration
}

type subscriber struct {
	name    string
	wants   func(eventName string) bool
	queue   chan Event
	handler Handler
	ctx     context.Context    // cancelled on removal; parents every delivery ctx
	cancel  context.CancelFunc // fires on removal, unblocking an in-flight handler that respects ctx
	wg      sync.WaitGroup
}

// NewBus builds an empty bus with default queue depth and delivery timeout.
func NewBus() *Bus {
	b := &Bus{queueCap: defaultQueueCap, deliverTimeout: defaultDeliverTimeout}
	empty := []*subscriber{}
	b.subs.Store(&empty)
	return b
}

// WithQueueCap sets the per-subscriber queue depth (drop-newest past it). Chainable.
func (b *Bus) WithQueueCap(n int) *Bus {
	if n > 0 {
		b.queueCap = n
	}
	return b
}

// WithDeliverTimeout bounds a single handler call. Chainable.
func (b *Bus) WithDeliverTimeout(d time.Duration) *Bus {
	if d > 0 {
		b.deliverTimeout = d
	}
	return b
}

// Publish delivers e to every matching subscriber. It NEVER blocks: a full queue drops the incoming
// event (drop-newest) and counts it. Safe to call after a tx commit on the hot path.
func (b *Bus) Publish(e Event) {
	for _, s := range *b.subs.Load() {
		if !s.wants(e.Name) {
			continue
		}
		select {
		case s.queue <- e:
		default:
			// Queue full: DROP-NEWEST — reject this event, keep the queued ones in order.
			metrics.PluginEventsDropped.WithLabelValues(s.name, e.Name, "backpressure").Inc()
		}
	}
}

// Subscribe registers a handler for events whose name satisfies wants ("*" via a func that always
// returns true). It returns a cancel func that removes the subscriber and stops its goroutine;
// cancel is idempotent. Subscribe/cancel are rare relative to Publish and never block it (copy-on-
// write swap of an immutable slice, read lock-free by Publish).
func (b *Bus) Subscribe(name string, wants func(string) bool, h Handler) (cancel func()) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &subscriber{
		name:    name,
		wants:   wants,
		queue:   make(chan Event, b.queueCap),
		handler: h,
		ctx:     ctx,
		cancel:  cancel,
	}

	b.writeMu.Lock()
	old := *b.subs.Load()
	next := make([]*subscriber, len(old), len(old)+1)
	copy(next, old)
	next = append(next, s)
	b.subs.Store(&next)
	b.writeMu.Unlock()

	s.wg.Add(1)
	go s.run(b.deliverTimeout)

	var once sync.Once
	return func() { once.Do(func() { b.remove(s) }) }
}

// remove takes s out of the subscriber set, stops its goroutine, and DISCARDS its queued events —
// counted as "shutdown" drops. This is the deliberate mid-teardown policy: a subscriber is removed
// only on a terminal plugin state (revert-before-death, per the loader), so the plugin is going
// away; delivering its backlog to a dying process is pointless. Cancelling s.ctx unblocks an
// in-flight handler that respects ctx, so remove returns promptly rather than waiting a full
// delivery timeout; everything still queued is then dropped and counted, never silent.
func (b *Bus) remove(s *subscriber) {
	b.writeMu.Lock()
	old := *b.subs.Load()
	next := make([]*subscriber, 0, len(old))
	for _, x := range old {
		if x != s {
			next = append(next, x)
		}
	}
	b.subs.Store(&next)
	b.writeMu.Unlock()

	s.cancel() // stop consuming AND cancel any in-flight delivery ctx, so remove returns promptly
	s.wg.Wait()
	for {
		select {
		case e := <-s.queue:
			metrics.PluginEventsDropped.WithLabelValues(s.name, e.Name, "shutdown").Inc()
		default:
			return
		}
	}
}

func (s *subscriber) run(deliverTimeout time.Duration) {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case e := <-s.queue:
			// A cancelled subscriber can still land HERE: select picks randomly among ready
			// cases, so once teardown cancels s.ctx both branches are ready and this one may win
			// repeatedly. Delivering is pointless (the delivery ctx derives from a dead parent)
			// and the failure branch below deliberately ignores teardown errors — so without this
			// check the event is consumed and NEVER counted, and remove()'s drain then finds an
			// empty queue. Count it exactly as the drain would, so an event cannot vanish
			// depending on which branch the scheduler picked.
			if s.ctx.Err() != nil {
				metrics.PluginEventsDropped.WithLabelValues(s.name, e.Name, "shutdown").Inc()
				return // teardown: the rest of the queue is drained and counted by remove()
			}
			dctx, cancel := context.WithTimeout(s.ctx, deliverTimeout)
			err := s.handler(dctx, e)
			cancel()
			// Count a genuine delivery failure — but NOT an error that is just our own teardown
			// cancelling the in-flight handler (that event is accounted for by the shutdown drain).
			if err != nil && s.ctx.Err() == nil {
				metrics.PluginEventsDropped.WithLabelValues(s.name, e.Name, "delivery").Inc()
			}
		}
	}
}
