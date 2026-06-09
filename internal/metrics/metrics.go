package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds the Prometheus registry and the instruments the API records.
type Metrics struct {
	registry            *prometheus.Registry
	duration            *prometheus.HistogramVec
	AnomalyInsufficient prometheus.Counter
	// ReadTimeouts counts reads that hit the per-request Redis deadline and were
	// shed with 503 instead of blocking — the fail-fast signal under storage slowness.
	ReadTimeouts prometheus.Counter
	// WritesDropped counts telemetry points dropped because the async write buffer
	// was full (the 202 accept-and-drop overflow path). Silent loss made visible.
	WritesDropped prometheus.Counter
}

// New builds an isolated registry (no Go runtime noise) with the
// http_request_duration_seconds histogram required by the smoke test.
func New() *Metrics {
	reg := prometheus.NewRegistry()
	dur := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request latency by operation.",
		Buckets: []float64{0.0005, 0.001, 0.002, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25},
	}, []string{"op"})
	ai := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "anomaly_insufficient_data_total",
		Help: "Anomaly requests answered 404 due to fewer than 8 points.",
	})
	rt := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "redis_read_timeout_total",
		Help: "Read requests shed with 503 after hitting the per-request Redis deadline.",
	})
	wd := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "telemetry_writes_dropped_total",
		Help: "Telemetry points dropped because the async write buffer was full.",
	})
	reg.MustRegister(dur, ai, rt, wd)
	// Pre-create the op children so http_request_duration_seconds is always
	// present in /metrics output, even before any request is observed (the
	// smoke test requires the "http_request" substring regardless of order).
	for _, op := range []string{"post", "batch", "range", "anomaly"} {
		dur.WithLabelValues(op)
	}
	return &Metrics{registry: reg, duration: dur, AnomalyInsufficient: ai, ReadTimeouts: rt, WritesDropped: wd}
}

// Observe records a request latency for op ∈ {post, batch, range, anomaly}.
func (m *Metrics) Observe(op string, seconds float64) {
	m.duration.WithLabelValues(op).Observe(seconds)
}

// Handler serves the registry in Prometheus text format (version=0.0.4).
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}
