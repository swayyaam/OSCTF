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
