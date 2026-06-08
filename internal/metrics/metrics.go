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
	reg.MustRegister(dur, ai)
	// Pre-create the op children so http_request_duration_seconds is always
	// present in /metrics output, even before any request is observed (the
	// smoke test requires the "http_request" substring regardless of order).
	for _, op := range []string{"post", "batch", "range", "anomaly"} {
		dur.WithLabelValues(op)
	}
	return &Metrics{registry: reg, duration: dur, AnomalyInsufficient: ai}
}

// Observe records a request latency for op ∈ {post, batch, range, anomaly}.
func (m *Metrics) Observe(op string, seconds float64) {
	m.duration.WithLabelValues(op).Observe(seconds)
}

// Handler serves the registry in Prometheus text format (version=0.0.4).
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}
