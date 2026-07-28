// Package metrics owns the Prometheus registry and the platform's custom metrics.
// The registry is the one piece of allowed global state (see 03-tech-stack.md);
// its init() only registers collectors.
package metrics

import (
	"net/http"

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
)

func init() {
	Registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		HTTPRequests, HTTPDuration, Submissions,
		WSConnections, Instances, RateLimitRejections,
		TeamInstances, InstanceSpawns, InstanceExpiries, InstanceCleanups, FlagSharingSignals,
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
