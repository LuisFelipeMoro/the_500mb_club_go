package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/batch"
	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/metrics"
	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/middleware"
	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/model"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// fakeStore mirrors Redis ZSET semantics: unique members keyed by blob, ordered
// by (score asc, member-bytes asc) like ZRANGE BYSCORE.
type fakeStore struct {
	mu   sync.Mutex
	data map[string]map[string]float64
}

func newFakeStore() *fakeStore { return &fakeStore{data: map[string]map[string]float64{}} }

func (f *fakeStore) AddMulti(_ context.Context, batches map[string][][]byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for dev, members := range batches {
		if f.data[dev] == nil {
			f.data[dev] = map[string]float64{}
		}
		for _, m := range members {
			f.data[dev][string(m)] = float64(model.DecodePoint(m).Ts)
		}
	}
	return nil
}

func (f *fakeStore) sorted(dev string) []string {
	keys := make([]string, 0, len(f.data[dev]))
	for k := range f.data[dev] {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		si, sj := f.data[dev][keys[i]], f.data[dev][keys[j]]
		if si != sj {
			return si < sj
		}
		return keys[i] < keys[j]
	})
	return keys
}

func (f *fakeStore) Range(_ context.Context, dev string, fromTS, toTS, offset, count int64) ([][]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var in []string
	for _, k := range f.sorted(dev) {
		s := f.data[dev][k]
		if s >= float64(fromTS) && s <= float64(toTS) {
			in = append(in, k)
		}
	}
	if offset > int64(len(in)) {
		offset = int64(len(in))
	}
	in = in[offset:]
	if count < int64(len(in)) {
		in = in[:count]
	}
	out := make([][]byte, len(in))
	for i := range in {
		out[i] = []byte(in[i])
	}
	return out, nil
}

func (f *fakeStore) LastN(_ context.Context, dev string, n int64) ([][]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := f.sorted(dev)
	// reverse for newest-first
	for i, j := 0, len(keys)-1; i < j; i, j = i+1, j-1 {
		keys[i], keys[j] = keys[j], keys[i]
	}
	if n < int64(len(keys)) {
		keys = keys[:n]
	}
	out := make([][]byte, len(keys))
	for i := range keys {
		out[i] = []byte(keys[i])
	}
	return out, nil
}

func (f *fakeStore) Ping(context.Context) error { return nil }
func (f *fakeStore) Close()                     {}

func newTestApp(t *testing.T) (*fiber.App, *fakeStore, *batch.Writer) {
	t.Helper()
	log := zap.NewNop()
	store := newFakeStore()
	m := metrics.New()
	w := batch.New(store, 1000, log)
	go w.Run()
	h := New(store, w, m, log)

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(middleware.Instrument("api-test", m))
	app.Post("/devices/:id/telemetry", h.PostTelemetry)
	app.Post("/devices/:id/telemetry/batch", h.PostBatch)
	app.Get("/devices/:id/telemetry", h.GetTelemetry)
	app.Get("/devices/:id/anomaly", h.GetAnomaly)
	app.Get("/readyz", h.Readyz)
	app.Get("/healthz", h.Healthz)
	app.Get("/metrics", h.MetricsHandler())
	return app, store, w
}

func do(t *testing.T, app *fiber.App, method, path, body string) (int, string, http.Header) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := app.Test(req, -1)
	require.NoError(t, err)
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func TestPostSingleThenRange(t *testing.T) {
	app, _, w := newTestApp(t)
	defer w.Close()

	status, _, hdr := do(t, app, "POST", "/devices/dev1/telemetry",
		`{"ts":1000,"lat":1,"lon":2,"ax":1,"ay":2,"az":3}`)
	assert.Equal(t, 202, status)
	assert.Equal(t, "api-test", hdr.Get("X-Instance-Id"), "X-Instance-Id stamped")

	time.Sleep(30 * time.Millisecond) // let batch writer flush

	status, body, _ := do(t, app, "GET", "/devices/dev1/telemetry?from=0&to=2000", "")
	assert.Equal(t, 200, status)
	assert.Contains(t, body, `"ts":1000`)
	assert.Contains(t, body, `"next_cursor":null`)
}

func TestBatchStatusGuards(t *testing.T) {
	app, _, w := newTestApp(t)
	defer w.Close()

	// >100 points -> 413 (checked before validation)
	var pts []string
	for i := 0; i < 101; i++ {
		pts = append(pts, `{"ts":1,"lat":0,"lon":0,"ax":1,"ay":1,"az":1}`)
	}
	status, _, _ := do(t, app, "POST", "/devices/d/telemetry/batch", `{"points":[`+strings.Join(pts, ",")+`]}`)
	assert.Equal(t, 413, status)

	// empty -> 400
	status, _, _ = do(t, app, "POST", "/devices/d/telemetry/batch", `{"points":[]}`)
	assert.Equal(t, 400, status)

	// invalid point -> 400
	status, _, _ = do(t, app, "POST", "/devices/d/telemetry/batch",
		`{"points":[{"ts":1,"lat":91,"lon":0,"ax":1,"ay":1,"az":1}]}`)
	assert.Equal(t, 400, status)

	// valid batch -> 202 accepted:2
	status, body, _ := do(t, app, "POST", "/devices/d/telemetry/batch",
		`{"points":[{"ts":1,"lat":0,"lon":0,"ax":1,"ay":1,"az":1},{"ts":2,"lat":0,"lon":0,"ax":1,"ay":1,"az":1}]}`)
	assert.Equal(t, 202, status)
	assert.Contains(t, body, `"accepted":2`)
}

func TestRangeValidation(t *testing.T) {
	app, _, w := newTestApp(t)
	defer w.Close()

	// missing from/to
	status, _, _ := do(t, app, "GET", "/devices/d/telemetry?to=10", "")
	assert.Equal(t, 400, status)
	// limit=0
	status, _, _ = do(t, app, "GET", "/devices/d/telemetry?from=0&to=10&limit=0", "")
	assert.Equal(t, 400, status)
	// limit=501
	status, _, _ = do(t, app, "GET", "/devices/d/telemetry?from=0&to=10&limit=501", "")
	assert.Equal(t, 400, status)
	// from>to
	status, _, _ = do(t, app, "GET", "/devices/d/telemetry?from=20&to=10", "")
	assert.Equal(t, 400, status)
	// bad device id (dot is not in ^[a-zA-Z0-9_-]{1,64}$)
	status, _, _ = do(t, app, "GET", "/devices/bad.id/telemetry?from=0&to=10", "")
	assert.Equal(t, 400, status)
}

func TestAnomalyEndpoint(t *testing.T) {
	app, _, w := newTestApp(t)
	defer w.Close()

	// fewer than 8 -> 404
	do(t, app, "POST", "/devices/anodev/telemetry/batch",
		`{"points":[{"ts":1,"lat":0,"lon":0,"ax":1,"ay":1,"az":1}]}`)
	time.Sleep(30 * time.Millisecond)
	status, _, _ := do(t, app, "GET", "/devices/anodev/anomaly", "")
	assert.Equal(t, 404, status)

	// seed 10 baseline zeros + 1 outlier (newest); 11 points -> z≈3.16 > 3
	var pts []string
	for i := 1; i <= 11; i++ {
		az := 0
		if i == 11 {
			az = 100 // newest, highest ts
		}
		pts = append(pts, jsonPoint(int64(i*1000), float64(az)))
	}
	do(t, app, "POST", "/devices/anodev2/telemetry/batch", `{"points":[`+strings.Join(pts, ",")+`]}`)
	time.Sleep(30 * time.Millisecond)

	status, body, hdr := do(t, app, "GET", "/devices/anodev2/anomaly", "")
	assert.Equal(t, 200, status)
	assert.Equal(t, "api-test", hdr.Get("X-Instance-Id"))
	var res struct {
		ZScore    float64 `json:"z_score"`
		Samples   int     `json:"samples"`
		Anomalous bool    `json:"anomalous"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &res))
	assert.Equal(t, 11, res.Samples)
	assert.True(t, res.Anomalous, "newest=100 vs 10 zeros must be anomalous: z=%v", res.ZScore)
}

func TestPaginationAcrossPages(t *testing.T) {
	app, _, w := newTestApp(t)
	defer w.Close()

	// 5 distinct-ts points
	var pts []string
	for i := 1; i <= 5; i++ {
		pts = append(pts, jsonPoint(int64(i*1000), 1))
	}
	do(t, app, "POST", "/devices/pg/telemetry/batch", `{"points":[`+strings.Join(pts, ",")+`]}`)
	time.Sleep(30 * time.Millisecond)

	seen := map[int64]bool{}
	url := "/devices/pg/telemetry?from=0&to=100000&limit=2"
	for i := 0; i < 10; i++ {
		_, body, _ := do(t, app, "GET", url, "")
		var resp struct {
			Points     []model.TelemetryPoint `json:"points"`
			NextCursor *string                `json:"next_cursor"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &resp))
		for _, p := range resp.Points {
			assert.False(t, seen[p.Ts], "duplicate ts %d", p.Ts)
			seen[p.Ts] = true
		}
		if resp.NextCursor == nil {
			break
		}
		url = "/devices/pg/telemetry?from=0&to=100000&limit=2&cursor=" + *resp.NextCursor
	}
	assert.Len(t, seen, 5, "all 5 points seen exactly once across pages")
}

func TestHealthReadyMetrics(t *testing.T) {
	app, _, w := newTestApp(t)
	defer w.Close()

	status, body, _ := do(t, app, "GET", "/healthz", "")
	assert.Equal(t, 200, status)
	assert.Contains(t, body, "ok")

	status, _, hdr := do(t, app, "GET", "/readyz", "")
	assert.Equal(t, 200, status)
	assert.Equal(t, "api-test", hdr.Get("X-Instance-Id"))

	status, body, _ = do(t, app, "GET", "/metrics", "")
	assert.Equal(t, 200, status)
	assert.Contains(t, body, "http_request", "smoke requires http_request substring")
}

func jsonPoint(ts int64, az float64) string {
	p := model.TelemetryPoint{Ts: ts, Lat: 0, Lon: 0, Ax: 0, Ay: 0, Az: az}
	b, _ := json.Marshal(p)
	return string(b)
}
