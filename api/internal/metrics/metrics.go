// Package metrics owns the Prometheus registry and the platform's custom metrics.
// The registry is the one piece of allowed global state (see 03-tech-stack.md);
// its init() only registers collectors.
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
)

// Registry is the process-wide Prometheus registry.
var Registry = prometheus.NewRegistry()

var (
	// HTTPRequests counts HTTP responses by route, method, and status.
	HTTPRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "osctf_http_requests_total",
		Help: "Total HTTP requests by route, method, and status.",
	}, []string{"route", "method", "status"})

	// HTTPDuration observes request latency by route and method.
	HTTPDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "osctf_http_request_duration_seconds",
		Help:    "HTTP request duration in seconds by route and method.",
		Buckets: prometheus.DefBuckets,
	}, []string{"route", "method"})

	// Submissions counts flag submissions by correctness.
	Submissions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "osctf_submissions_total",
		Help: "Total flag submissions by correctness.",
	}, []string{"correct"})

	// WSConnections gauges live WebSocket connections.
	WSConnections = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "osctf_ws_connections",
		Help: "Current number of open WebSocket connections.",
	})

	// WSRejections counts WebSocket handshakes refused by a connection cap or the
	// handshake rate limit, labelled by which limit fired.
	WSRejections = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "osctf_ws_rejections_total",
		Help: "WebSocket handshakes rejected, by limit (global, per_key, handshake_rate).",
	}, []string{"limit"})

	// WSReadPumpPanics counts panics recovered in a WebSocket read pump. Any nonzero
	// value is a bug worth chasing — a recovered panic must leave a trace, not vanish.
	WSReadPumpPanics = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "osctf_ws_readpump_panics_total",
		Help: "Panics recovered in a WebSocket read pump (should always be 0).",
	})

	// Instances gauges challenge instances by state.
	Instances = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "osctf_instances",
		Help: "Challenge instances by state.",
	}, []string{"state"})

	// RateLimitRejections counts rate-limit rejections by scope.
	RateLimitRejections = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "osctf_ratelimit_rejections_total",
		Help: "Rate-limit rejections by scope.",
	}, []string{"scope"})

	// TeamInstances gauges per-team challenge instances by state.
	TeamInstances = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "osctf_team_instances",
		Help: "Per-team challenge instances by state.",
	}, []string{"state"})

	// InstanceSpawns counts per-team instance starts.
	InstanceSpawns = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "osctf_instance_spawns_total",
		Help: "Per-team instance starts.",
	})

	// InstanceExpiries counts TTL expirations.
	InstanceExpiries = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "osctf_instance_expiries_total",
		Help: "Per-team instances destroyed by TTL expiry.",
	})

	// InstanceCleanups counts destroys by reason (stop, event-end).
	InstanceCleanups = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "osctf_instance_cleanups_total",
		Help: "Per-team instances destroyed, by reason.",
	}, []string{"reason"})

	// FlagSharingSignals counts submissions of another team's per-instance flag.
	FlagSharingSignals = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "osctf_flag_sharing_signals_total",
		Help: "Flag-sharing signals raised (another team's per-instance flag submitted).",
	})

	// UnadoptedContainers gauges managed containers whose osctf.instance_id label is
	// missing/unparseable — reconcile cannot identify them, so they hold a port with
	// nothing surfacing them. Set each reconcile pass.
	UnadoptedContainers = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "osctf_unadopted_containers",
		Help: "Managed containers with an unresolvable instance_id label (held ports, no row).",
	})

	// UnadoptedNetworks gauges per-team bridges with no resolvable team_id (never
	// GC'd, surfaced for manual cleanup). Set each reconcile pass.
	UnadoptedNetworks = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "osctf_unadopted_networks",
		Help: "Per-team bridges with no resolvable team_id (never GC'd).",
	})

	// ReconcileActions gauges the actions a reconcile pass emitted; ReconcileGraceSkipped
	// gauges rows it left alone for grace. A loop skipping 100% of rows (e.g. clock
	// skew) shows grace-skipped high and actions zero. Gauges are last-value, so pair
	// them with the counter + last-success timestamp below for liveness/history.
	ReconcileActions      = prometheus.NewGauge(prometheus.GaugeOpts{Name: "osctf_reconcile_actions", Help: "Actions emitted by the last reconcile pass."})
	ReconcileGraceSkipped = prometheus.NewGauge(prometheus.GaugeOpts{Name: "osctf_reconcile_grace_skipped", Help: "Rows the last reconcile pass skipped due to grace."})

	// ReconcileActionsTotal counts actions by kind across passes, so history survives
	// between scrapes (unlike the per-pass gauge above).
	ReconcileActionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "osctf_reconcile_actions_total",
		Help: "Reconcile actions applied, by kind.",
	}, []string{"kind"})

	// ReconcileFutureRows counts rows seen with updated_at ahead of the clock — a
	// clock-skew anomaly that would otherwise make grace no-op silently.
	ReconcileFutureRows = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "osctf_reconcile_future_rows_total",
		Help: "Rows observed with updated_at ahead of the reconcile clock (skew anomaly).",
	})

	// *LastSuccess gauge the Unix time each periodic sweep last completed. A gauge
	// holding its last value hides a dead/wedged goroutine; alert on staleness
	// (time() - last_success) instead.
	ReconcileLastSuccess = prometheus.NewGauge(prometheus.GaugeOpts{Name: "osctf_reconcile_last_success_timestamp_seconds", Help: "Unix time the reconcile pass last completed."})
	ExpiryLastSuccess    = prometheus.NewGauge(prometheus.GaugeOpts{Name: "osctf_expiry_last_success_timestamp_seconds", Help: "Unix time the TTL-expiry pass last completed."})
	ReapLastSuccess      = prometheus.NewGauge(prometheus.GaugeOpts{Name: "osctf_reap_last_success_timestamp_seconds", Help: "Unix time the stale-row reaper last completed."})

	// ScoreboardStaleReads counts reads that found the cached board behind the solve log
	// and recomputed before serving (the read-repair path). "How often is the board
	// behind" is this number, not a soak flake. A high rate means the per-solve recompute
	// often isn't landing.
	ScoreboardStaleReads = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "osctf_scoreboard_stale_reads_total",
		Help: "Scoreboard reads that detected a stale cache and recomputed before serving.",
	})

	// ScoreboardStaleServed counts reads that detected staleness but served the stale
	// board because inline repair exceeded its budget (the bounded fallback). Any nonzero
	// value is a board briefly served behind the log — alert on it.
	ScoreboardStaleServed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "osctf_scoreboard_stale_served_total",
		Help: "Scoreboard reads served stale because inline read-repair exceeded its budget.",
	})

	// PluginCallDuration observes host→plugin call latency by plugin and method. The failure
	// mode is a TAIL (a plugin that is correct but slow), so the buckets resolve the region
	// around the call timeout — DefBuckets tops out at 10 and is too coarse above 1s to see a
	// consistently-4s plugin. Recorded for successful calls too, so a slow-but-never-failing
	// plugin is visible here without reading logs.
	PluginCallDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "osctf_plugin_call_duration_seconds",
		Help:    "Host→plugin call duration in seconds by plugin and method (tail-resolving buckets).",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 4, 8, 10},
	}, []string{"plugin", "method"})

	// PluginInflight gauges concurrent host→plugin calls per plugin. It must return to 0 after
	// every plugin drains — a value stuck above 0 after a stop/reload is a leaked in-flight slot
	// (the port-leak shape), so alert on a nonzero idle value.
	PluginInflight = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "osctf_plugin_inflight",
		Help: "Current concurrent host→plugin calls, by plugin.",
	}, []string{"plugin"})

	// PluginInflightShed counts calls shed at an in-flight cap, by plugin and which cap fired:
	// "per_plugin" (one plugin monopolising its own budget) vs "global" (all plugins collectively
	// exhausting the shared budget) — different causes needing different operator responses.
	PluginInflightShed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "osctf_plugin_inflight_shed_total",
		Help: "Host→plugin calls shed at an in-flight cap, by plugin and cap level (per_plugin, global).",
	}, []string{"plugin", "level"})

	// PluginScoreRecordFailures counts post-commit scoring-record WRITE failures — the solve
	// committed but its scored_value did not land, leaving a MISSING record. This is the write-path
	// durability signal; the repair worker backfills it, but a rising rate means writes are failing.
	PluginScoreRecordFailures = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "osctf_plugin_score_record_failures_total",
		Help: "Post-commit plugin-scoring record writes that failed (leaving a missing record).",
	})

	// PluginScoresMissing gauges valid plugin-scored solves with NO recorded value and NO scored_by
	// — an ABSENCE (the post-commit write never landed), resolved to 0 on the board until repaired.
	// ALERT on a sustained nonzero value: it means the write path is broken and the board is
	// under-counting real solves. Distinct from pending (which is an expected deferral). Refreshed
	// each repair-worker tick.
	PluginScoresMissing = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "osctf_plugin_scores_missing",
		Help: "Valid plugin-scored solves with a missing scoring record (absent write) — alert if sustained.",
	})

	// PluginScoresPending gauges plugin-scored solves recorded as 'pending' (plugin was down, no
	// fallback) — a DEFINITE state resolving to 0, expected to clear when the plugin recovers and
	// the repair worker fills the value. Not itself an alert; a pending count that never drains is.
	PluginScoresPending = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "osctf_plugin_scores_pending",
		Help: "Plugin-scored solves recorded as pending (deferred value), expected to clear on plugin recovery.",
	})

	// PluginScoresRepaired counts scoring records the off-read-path worker backfilled (missing or
	// pending → a value). A healthy small trickle is normal; a spike tracks a write-path outage.
	PluginScoresRepaired = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "osctf_plugin_scores_repaired_total",
		Help: "Missing/pending plugin-scoring records backfilled by the repair worker.",
	})

	// PluginEventsDropped counts notification-bus deliveries dropped — NEVER silent. The bus is
	// best-effort and fails open (a drop never gates the action that published the event), but every
	// drop is observable here, by subscriber, event, and reason: "backpressure" (the subscriber's
	// bounded queue was full — a slow plugin, drop-newest), "delivery" (the plugin's Notify call
	// errored), or "shutdown" (queued events discarded when the subscriber was removed on a terminal
	// plugin state). A rising backpressure rate for one subscriber is the slow-plugin signal.
	PluginEventsDropped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "osctf_plugin_events_dropped_total",
		Help: "Notification-bus events dropped, by subscriber, event, and reason (backpressure, delivery, shutdown).",
	}, []string{"name", "event", "reason"})

	// WSBroadcastsDropped counts WebSocket broadcasts dropped because the hub's ingress channel was
	// full, by frame kind ("scoreboard" — superseded by the next snapshot, benign; "phase" — a
	// transition frame lost, which read-repair/reconnect eventually corrects but is worth watching).
	// These drops existed and were UNCOUNTED before v0.3; anyone who ran an event had invisible drops.
	WSBroadcastsDropped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "osctf_ws_broadcasts_dropped_total",
		Help: "WebSocket broadcasts dropped at the hub ingress (full channel), by frame kind (scoreboard, phase).",
	}, []string{"kind"})
)

// MarkSuccess sets g to the current wall-clock time (a liveness heartbeat for a
// periodic pass).
func MarkSuccess(g prometheus.Gauge) { g.Set(float64(time.Now().Unix())) }

func init() {
	Registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		HTTPRequests, HTTPDuration, Submissions,
		WSConnections, WSRejections, WSReadPumpPanics, Instances, RateLimitRejections,
		TeamInstances, InstanceSpawns, InstanceExpiries, InstanceCleanups, FlagSharingSignals,
		UnadoptedContainers, UnadoptedNetworks, ReconcileActions, ReconcileGraceSkipped, ReconcileFutureRows,
		ReconcileActionsTotal, ReconcileLastSuccess, ExpiryLastSuccess, ReapLastSuccess,
		ScoreboardStaleReads, ScoreboardStaleServed,
		PluginCallDuration, PluginInflight, PluginInflightShed,
		PluginScoreRecordFailures, PluginScoresMissing, PluginScoresPending, PluginScoresRepaired,
		PluginEventsDropped, WSBroadcastsDropped,
	)
}

// Handler serves the metrics registry.
func Handler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{})
}

// GaugeValue reads the current value of a gauge (for the admin stats tile).
func GaugeValue(g prometheus.Gauge) float64 {
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		return 0
	}
	return m.GetGauge().GetValue()
}

// CounterValue reads the current value of a counter.
func CounterValue(c prometheus.Counter) float64 {
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		return 0
	}
	return m.GetCounter().GetValue()
}
