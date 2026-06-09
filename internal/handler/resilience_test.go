package handler

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/batch"
	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/metrics"
	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

// slowStore blocks every read until the request context is cancelled, so a
// handler with a tight ReadTimeout always trips the deadline.
type slowStore struct{}

func (slowStore) AddMulti(context.Context, map[string][][]byte) error { return nil }
func (slowStore) Ping(context.Context) error                          { return nil }
func (slowStore) Close()                                              {}
func (slowStore) Range(ctx context.Context, _ string, _, _, _, _ int64) ([][]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (slowStore) LastN(ctx context.Context, _ string, _ int64) ([][]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// metricLine scrapes /metrics and returns the body for substring assertions.
func metricsBody(t *testing.T, app *fiber.App) string {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest("GET", "/metrics", nil), 2000)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// G3: a read that outlives ReadTimeout is shed with 503 and counted, instead of
// blocking up to the server WriteTimeout.
func TestReadTimeoutSheds503(t *testing.T) {
	m := metrics.New()
	h := New(slowStore{}, nil, m, zap.NewNop(), 10*time.Millisecond)
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/devices/:id/telemetry", h.GetTelemetry)
	app.Get("/devices/:id/anomaly", h.GetAnomaly)
	app.Get("/metrics", h.MetricsHandler())

	for _, path := range []string{
		"/devices/dev/telemetry?from=0&to=9999999999999",
		"/devices/dev/anomaly",
	} {
		resp, err := app.Test(httptest.NewRequest("GET", path, nil), 2000)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if resp.StatusCode != fiber.StatusServiceUnavailable {
			t.Errorf("%s: status = %d, want 503", path, resp.StatusCode)
		}
	}
	if body := metricsBody(t, app); !strings.Contains(body, "redis_read_timeout_total 2") {
		t.Errorf("redis_read_timeout_total 2 not in /metrics:\n%s", body)
	}
}

// G4: when the async write buffer is full, ingest still answers 202 (FR-1) but
// the dropped points are counted instead of vanishing silently.
func TestWriteOverflowCounted(t *testing.T) {
	m := metrics.New()
	// bufSize 0 + never Run() → the non-blocking Push always overflows.
	w := batch.New(newFakeStore(), 0, time.Second, zap.NewNop())
	h := New(newFakeStore(), w, m, zap.NewNop(), 250*time.Millisecond)
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/devices/:id/telemetry", h.PostTelemetry)
	app.Post("/devices/:id/telemetry/batch", h.PostBatch)
	app.Get("/metrics", h.MetricsHandler())

	post := func(path, body string) int {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req, 2000)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		return resp.StatusCode
	}

	if s := post("/devices/dev/telemetry", `{"ts":1,"lat":0,"lon":0,"ax":0,"ay":0,"az":9.8}`); s != fiber.StatusAccepted {
		t.Fatalf("single status = %d, want 202", s)
	}
	if s := post("/devices/dev/telemetry/batch", `{"points":[{"ts":1,"lat":0,"lon":0,"ax":0,"ay":0,"az":9.8},{"ts":2,"lat":0,"lon":0,"ax":0,"ay":0,"az":9.8}]}`); s != fiber.StatusAccepted {
		t.Fatalf("batch status = %d, want 202", s)
	}
	if body := metricsBody(t, app); !strings.Contains(body, "telemetry_writes_dropped_total 3") { // 1 single + 2 batch
		t.Errorf("telemetry_writes_dropped_total 3 not in /metrics:\n%s", body)
	}
}
