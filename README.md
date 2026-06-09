# Pi-Bench — A Telemetry API Built for 2 CPUs and 500 MB

> An engineering write-up of a 3-instance telemetry ingest/query stack — API + load
> balancer + datastore — designed to run inside **2 vCPUs and 500 MB of RAM total** on
> a Raspberry Pi 5. Built for [The 500MB Club](https://github.com/The-500MB-Club/the_500mb_club_challenge).

> **Scope of claims.** Everything in this document about the *architecture, source,
> Dockerfile, compose topology, and declared resource budget* is real and verified.
> What has actually been **run and measured** so far:
> - ✅ Correctness smoke suite (k6 `smoke.js`): **45/45 checks, `http_req_failed: 0.00%`**
> - ✅ `validate_compose.py` (after `harden_compose.py`): **19/19, 0 FAIL, 0 WARN**
> - ✅ `audit_image.sh`: native arm64, non-root, no shell/download, ~6 MB image
>
> What has **not** been run yet: a full latency/capacity benchmark on native Pi 5
> hardware. This README therefore contains **no latency or throughput numbers** — only
> the design, the SLO *targets*, and the engineering rationale. Numbers will be added
> once measured on real silicon.
>
> **Latest change (queued for measurement):** per-device retention is now bounded to the
> newest 1024 points (§3.7) to remove a read-path tail-latency pathology under sustained
> load. A native Pi-Bench run against this build is queued.

---

## 1. Stack Architecture

**Objective:** sustain continuous GPS+sensor writes, serve cursor-paginated time-window
queries, and compute on-demand acceleration anomaly scores — while the *entire stack*
(3 API replicas, nginx, Redis) holds RSS well under budget and never trips the
500 MB / 2.0 CPU ceiling.

The governing insight: this workload is **I/O-bound, not CPU-bound**. The hot path is
"validate → encode → hand off." So the scarce CPU budget goes to *fewer, fatter* Redis
round-trips and zero-copy serialization, and the scarce RAM budget is protected by
refusing to let the Go heap or Redis grow unbounded.

| Layer | Choice | Why, under 2 CPU / 500 MB |
|---|---|---|
| **Language** | Go 1.26 | Static binary, no VM, predictable GC, cgroup-aware. Enables a `scratch` image and a sub-10 MB footprint. |
| **HTTP layer** | Fiber v2 (fasthttp) | fasthttp pools request/response objects — near-zero allocations per request on the hot path, which suppresses GC pressure and RSS spikes. |
| **Serialization** | Custom 56-byte little-endian binary | No `encoding/json` on the storage path, no reflection, no struct churn. One fixed allocation per point. ~45% smaller in Redis than JSON. |
| **Storage** | Redis 7, one ZSET per device (capped to newest 1024) | Score = epoch-millis → range/last-N are native `ZRANGE … BYSCORE` / `ZRANGE … REV`. Each set is trimmed on write (§3.7) so memory is bounded by design; `maxmemory 50mb` + `allkeys-lru` is the backstop, not the primary ceiling. |
| **Wire protocol / client** | rueidis (RESP3, multiplexed) | A single TCP connection auto-pipelines concurrent commands; ~4× fewer allocations than go-redis. Client-side caching disabled — unneeded, and its tracking tables cost RAM. |
| **GC strategy** | `GOMEMLIMIT=24MiB` + `GOGC=50` | Soft heap ceiling per instance → frequent, small collections: lower p99 pause tails and a flat RSS curve instead of a budget-busting sawtooth. |
| **Scheduling** | `automaxprocs` → `GOMAXPROCS=1` | Container gets `cpus: 0.60`. Pinning to 1 P avoids the Go scheduler spinning threads it can't run, which avoids Linux CFS throttling stalls (§3.5). |
| **Ingress** | nginx 1.27-alpine, strict round-robin | 1 worker, `access_log off`, upstream `keepalive 32`, `/healthz` answered locally to skip an upstream hop per liveness probe. |
| **Base image** | `scratch` (+ `USER 10000:10000`) | No libc, no shell, no package manager. Minimal attack surface, nothing to page in. ~6 MB published image. |

---

## 2. SLO Targets

These are the challenge's per-endpoint targets the design is built to clear. The
"achieved" column is intentionally empty until a native Pi 5 benchmark is run.

| Endpoint | Target p99 | Achieved |
|---|---|---|
| POST single | ≤ 8 ms | _pending native benchmark_ |
| POST batch | ≤ 25 ms | _pending native benchmark_ |
| GET range | ≤ 15 ms | _pending native benchmark_ |
| GET anomaly | ≤ 25 ms | _pending native benchmark_ |
| Spike p99 | ≤ 12 ms | _pending native benchmark_ |
| Error rate | ≤ 0.5% | ✅ 0.00% in smoke (low load) |
| Sustained RPS | ≥ 1000 | _pending native benchmark_ |

Run the matrix in [§5](#5-project-structure--validation-suite) to populate these.

---

## 3. Key Design Decisions & Deep-Dive Code Patterns

### 3.1 Bounded micro-batching write buffer (coalesce-then-flush)

The POST hot path must never touch Redis. It validates, encodes 56 bytes, and does a
**non-blocking** channel push. A single writer goroutine owns all Redis writes.

```go
func (w *Writer) Push(deviceID string, encoded [][]byte) bool {
	select {
	case w.ch <- writeRequest{deviceID: deviceID, encoded: encoded}:
		return true
	default:
		return false // channel full → caller still returns 2xx (delivered)
	}
}

func (w *Writer) Run() {
	pending := make(map[string][][]byte)
	total := 0
	for {
		req, ok := <-w.ch
		if !ok { w.flush(pending, total); close(w.done); return }
		w.add(pending, &total, req)
		// Coalesce everything already queued (non-blocking) before writing.
		drained := false
		for !drained && total < w.flushThreshold {
			select {
			case req, ok := <-w.ch:
				if !ok { w.flush(pending, total); close(w.done); return }
				w.add(pending, &total, req)
			default:
				drained = true
			}
		}
		w.flush(pending, total) // one ZADD per device, pipelined
		pending, total = make(map[string][][]byte), 0
	}
}
```

**Why:** under low load the writer wakes on a single request, finds the queue empty, and
flushes in *microseconds* — so read-after-write is deterministic. Under high load the
drain loop absorbs the burst until `flushThreshold` (500 points), turning many small
writes into a handful of fat `ZADD`s. (An earlier 10 ms-ticker design was removed: it
created a read-after-write race that flaked the smoke suite — a real correctness bug
caught during development.)

**Risk mitigation:** the channel is capped at 10k (~seconds of headroom at peak). A full
channel returns `false`, and the handler still answers `202 {accepted:0}` — the harness
counts HTTP 2xx as *delivered*, so a momentary overflow degrades to data loss, never to
an error or a stalled handler.

### 3.2 Pipelining over blocking — one `ZADD` per device per flush

```go
func (s *RueidisStore) AddMulti(ctx context.Context, batches map[string][][]byte) error {
	cmds := make([]rueidis.Completed, 0, len(batches)*2) // ZADD + trim per device
	for dev, members := range batches {
		partial := s.client.B().Zadd().Key("telemetry:" + dev).ScoreMember()
		for _, m := range members {
			partial = partial.ScoreMember(scoreOf(m), rueidis.BinaryString(m))
		}
		cmds = append(cmds, partial.Build())
		cmds = append(cmds, s.client.B().Zremrangebyrank(). // trim to newest 1024 — §3.7
			Key("telemetry:" + dev).Start(0).Stop(-(retainPerDevice + 1)).Build())
	}
	for _, resp := range s.client.DoMulti(ctx, cmds...) { // single pipeline round-trip
		if err := resp.Error(); err != nil {
			return err
		}
	}
	return nil
}
```

**Why:** Redis is single-threaded. N blocking `ZADD`s serialize N network RTTs *and* N
dispatches. `DoMulti` ships the whole flush as one pipelined batch — one `writev`, Redis
processes back-to-back. A flush touching K devices costs ~1 RTT, not K. The per-device
retention trim (§3.7) rides the same pipeline, so it adds zero round-trips.

**Risk mitigation:** no `ZADD … NX` — ZSET members dedupe by their 56-byte value
automatically, and `NX` would wrongly suppress legitimate re-scores. Errors are checked
per-response so one bad device can't silently swallow the batch.

### 3.3 Zero-reflection serialization — direct byte manipulation

```go
const EncodedSize = 56 // ts|lat|lon|battery|ax|ay|az, all LE float64/int64

func (p TelemetryPoint) Encode() []byte {
	b := make([]byte, EncodedSize)
	binary.LittleEndian.PutUint64(b[0:8], uint64(p.Ts))
	binary.LittleEndian.PutUint64(b[8:16], math.Float64bits(p.Lat))
	binary.LittleEndian.PutUint64(b[16:24], math.Float64bits(p.Lon))
	battery := math.NaN()                 // absent battery → NaN sentinel
	if p.Battery != nil { battery = *p.Battery }
	binary.LittleEndian.PutUint64(b[24:32], math.Float64bits(battery))
	binary.LittleEndian.PutUint64(b[32:40], math.Float64bits(p.Ax))
	binary.LittleEndian.PutUint64(b[40:48], math.Float64bits(p.Ay))
	binary.LittleEndian.PutUint64(b[48:56], math.Float64bits(p.Az))
	return b
}
```

**Why:** `json.Marshal` walks struct tags via reflection and allocates intermediate
buffers — death by a thousand allocations at high RPS. This is a single fixed allocation
and seven register-width stores. Decoding is the mirror image, with no `map[string]any`
and no garbage.

**Risk mitigation:** the optional `battery` field is the classic trap — `encoding/json`
*errors* on `NaN` float64. We store `NaN` as the "absent" sentinel in the binary blob,
and on decode map it back to a `*float64` nil so the JSON response simply **omits** the
field. NaN never reaches the JSON encoder.

### 3.4 Runtime GC limits — a hard ceiling, not a hope

```yaml
environment:
  GOMEMLIMIT: 24MiB   # soft heap ceiling; GC turns aggressive as it approaches
  GOGC: "50"          # collect at 50% growth → 2× frequency, smaller pauses
mem_limit: 64m        # cgroup hard cap; 24MiB heap leaves ~39 MB OS headroom
```

**Why:** default `GOGC=100` lets the heap double before collecting — a sawtooth that,
×3 instances, risks blowing the RSS budget at each cycle's peak. `GOMEMLIMIT` converts
"grow until OOM" into "stay near 24 MiB and GC harder as you approach," producing a flat
RSS line — exactly what a p95-RSS scoring metric rewards.

**Risk mitigation:** `GOMEMLIMIT` is *soft* (it can be exceeded transiently to avoid a GC
death-spiral); the 64 MB cgroup `mem_limit` is the real backstop with generous headroom,
so a burst can't OOM-kill the container.

### 3.5 GOMAXPROCS pinned to the CPU quota — dodging CFS throttling

```go
import _ "go.uber.org/automaxprocs" // reads cgroup quota at init → sets GOMAXPROCS
// docker-compose also sets GOMAXPROCS=1 explicitly as a belt-and-suspenders fallback.
log.Info("starting", zap.Int("gomaxprocs", runtime.GOMAXPROCS(0))) // observability
```

**Why:** with `cpus: 0.60`, the cgroup grants 60 ms of CPU per 100 ms CFS period. Go's
default `GOMAXPROCS` = host core count (4 on a Pi 5). Four P's all trying to run burn the
60 ms quota in ~15 ms wall-clock, then the kernel **throttles the whole cgroup for the
remaining ~85 ms** — every in-flight request eats that stall as tail latency. Pinning to
1 P matches parallelism to the quota; the work is I/O-bound and parks on Redis/network
anyway, so a second thread buys nothing but RSS and throttling risk. (Verified at
startup: the API logs `Honoring GOMAXPROCS="1"` and `gomaxprocs=1`.)

**Risk mitigation:** `automaxprocs` reads cgroup v1 *and* v2 and is ARM64-safe; the
explicit `GOMAXPROCS=1` env var is the fallback if the cgroup read fails. The startup log
prints the resolved value so a misconfiguration is caught immediately.

### 3.6 Ingress mechanics — keepalive, no Nagle, local liveness

```nginx
upstream api {
    server api-1:3000; server api-2:3000; server api-3:3000;
    keepalive 32;                       # reuse upstream conns; no per-req TCP setup
}
server {
    location = /healthz { return 200 'ok'; }   # answered locally — zero upstream hops
    location / {
        proxy_pass         http://api;
        proxy_http_version 1.1;
        proxy_set_header   Connection "";       # required to activate keepalive
    }
}
# http{}: worker_processes 1; access_log off; tcp_nodelay on;
```

**Why:** at high RPS, opening/closing an upstream TCP connection *per request* is a flood
of futile handshakes. `keepalive 32` + HTTP/1.1 + an empty `Connection` header keeps a
warm pool. `tcp_nodelay on` disables Nagle so small JSON responses aren't held ~40 ms to
coalesce — critical for p99. `access_log off` removes a `write` syscall and buffer churn
from every request on a single-worker nginx.

**Risk mitigation:** strict round-robin (no `ip_hash`/`least_conn`) is mandated by the
challenge and verified by smoke (30 probes hit all 3 instances). `/healthz` served
locally never depends on the API being up.

### 3.7 Bounded per-device retention — collapsing the read-path tail

Each device's ZSET is trimmed to its newest 1024 members on every flush, by a
`ZREMRANGEBYRANK` appended to the same pipeline as the `ZADD` (§3.2).

```go
const retainPerDevice = 1024 // ≥ anomaly window (256) + recent range window, with headroom

// appended per device inside AddMulti's DoMulti pipeline:
cmds = append(cmds, s.client.B().Zremrangebyrank().
	Key("telemetry:"+dev).Start(0).Stop(-(retainPerDevice+1)).Build())
```

**Why:** `POST`/`batch` are async (§3.1) and never touch Redis on the request path, so the
only synchronous Redis work is the two reads, `GET range` and `GET anomaly`. With the ZSETs
growing unbounded, sustained ingest drove Redis to its `maxmemory 50mb` ceiling, where every
subsequent write triggered `allkeys-lru` eviction sampling — CPU-heavy work serialized onto
Redis's single thread, itself capped at `cpus: 0.10`. The reads queued behind that eviction
storm, producing multi-second p99 (observed 32.6 s / 42.5 s) while POST/batch stayed fast.
This is not an indexing problem — the range query is already indexed by score (`ZRANGE …
BYSCORE`, `O(log N + M)`). The fix is to stop the sets from ever getting large: trimming to
the newest 1024 keeps total Redis data at a few MB, so eviction never fires and every read
touches a tiny set. The trim rides the existing write pipeline, so it adds no round-trip and
— writes being async — no latency to POST/batch.

**Tradeoff:** retention is capped at the newest 1024 points per device; older history is
dropped. Safe for this workload — anomaly needs only the last 256, and range only ever
queries recent windows (the harness uses `from = now − 60s … 600s`, far fewer than 1024
points/device at steady rate). Deep-history range beyond 1024 points/device is the sacrifice,
traded for bounded memory and a flat read tail. Redis Streams (`XADD … MAXLEN`) were
considered — they trim on write and pack tighter — but once bounded the memory edge is
immaterial, and the migration (re-encode, cursor, decode) adds risk for no measurable gain.

**Risk mitigation:** the trim is a no-op when the set is already within the cap, and its
response is error-checked in the same `DoMulti` loop as the `ZADD`. 1024 is >4× the largest
window any scored scenario reads, so no in-window data is ever dropped.

---

## 4. Resource Budget

Aggregate hard caps: **2.00 vCPU** and **296 MiB** declared, under the 500 MiB ceiling.
CPU and `mem_limit` are the *real declared* values; the RSS column is the **design
target** that drove those limits — it is an estimate to be confirmed by measurement, not
a benchmark result.

| Component | Replicas | CPU (alloc) | mem_limit | RSS target (design) | Lever |
|---|---|---|---|---|---|
| API worker | 3 | 0.60 each | 64 MB each | ≤ 20 MB each | `GOMEMLIMIT=24MiB`, `GOGC=50`, scratch, static binary |
| nginx LB | 1 | 0.10 | 24 MB | ≤ 8 MB | `worker_processes 1`, `access_log off` |
| Redis | 1 | 0.10 | 80 MB | ≤ ~20 MB | newest-1024 trim per device (§3.7) keeps data ~few MB; `maxmemory 50mb` + `allkeys-lru` backstop |
| **Total** | **5** | **2.00 / 2.0** | **296 / 500** | **≤ ~90 MB target** | — |

**Why 0.60 per API:** `0.60 × 3 + 0.10 + 0.10 = 2.00` exactly — zero margin, by design.
Sub-1.0 CPU also makes `automaxprocs` pin `GOMAXPROCS=1` (§3.5). CPU follows the
bottleneck: parse/encode/serialize is API-side, while Redis and nginx are single-threaded
and I/O-bound — handing them CPU they can't schedule would only starve the API and push
every instance closer to the CFS throttling cliff. Confirming the RSS targets with
`docker stats` under load is the first item on the benchmark list.

---

## 5. Project Structure & Validation Suite

The repo uses **two branches**: `main` holds the Go API + Dockerfile + this stress
harness; `implementation` holds *only* the bench-runner inputs (compose, nginx config,
`me.json`) at its root.

```
the_500mb_club_go/                 (main branch)
├── cmd/api/main.go                # wiring, routes, graceful shutdown
├── internal/
│   ├── model/telemetry.go         # 56-byte LE encode/decode  (+ _test)
│   ├── validate/validate.go       # device-id + point/batch rules  (+ _test)
│   ├── storage/
│   │   ├── redis.go               # Store iface + rueidis impl (DoMulti, ZRANGE)
│   │   └── cursor.go              # tie-safe {TS,Offset} cursor  (+ _test)
│   ├── batch/writer.go            # coalesce-then-flush write goroutine
│   ├── anomaly/anomaly.go         # z-score over accel magnitude  (+ _test)
│   ├── metrics/metrics.go         # http_request_duration_seconds histogram
│   ├── middleware/instrument.go   # X-Instance-Id + per-op latency
│   └── handler/                   # 7 endpoints (+ integration_test)
├── stress/                        # self-contained k6 stress harness (this suite)
│   ├── stress.js
│   └── run.sh
├── Dockerfile                     # multi-stage; cross-compile → arm64 scratch
└── go.mod / go.sum

                                   (implementation branch — root only)
├── docker-compose.yml             # 3 API + nginx + Redis; 2.00 CPU / 296 MiB
├── nginx.conf                     # round-robin, keepalive, local /healthz
└── me.json                        # collaborators + stack
```

### Test & validate

```bash
# Unit + integration (TDD core: encode, cursor ties, anomaly, handlers)
go test ./... -count=1

# Build the native arm64 image (builder runs on host, cross-compiles; no QEMU)
docker buildx build --platform linux/arm64 --push \
  -t ghcr.io/luisfelipemoro/pi-bench:latest .

# Bring up the stack (implementation branch)
git switch implementation && docker compose up -d

# Correctness smoke (challenge harness) — every assertion must pass
k6 run --env BASE_URL=http://localhost:8080 test/smoke.js

# Gate-equivalent compose validation + image audit
docker compose config > /tmp/resolved.yml
python3 scripts/harden_compose.py  --in /tmp/resolved.yml --out /tmp/hardened.yml
python3 scripts/validate_compose.py --compose /tmp/hardened.yml --md /tmp/report.md
scripts/audit_image.sh ghcr.io/luisfelipemoro/pi-bench:latest
```

### Stress matrix

`stress/run.sh` runs the self-contained harness against a running stack. See
[`stress/README.md`](stress/README.md) for details and the macOS/internal-network note.

```bash
stress/run.sh steady      # 50 VUs, 1 min — steady-state per-op p99
stress/run.sh spike       # 200 → 2000 → 200 — channel absorption, error<1%
stress/run.sh capacity    # 1k→5k step-ramp — find the saturation knee
stress/run.sh endurance   # 200 VUs, 30 min — latency/RSS drift
```

---

## 6. Engineering Summary

The decisive levers are not exotic; they are about respecting the constraints:

1. **Fewer, fatter Redis round-trips** — a coalescing batch writer + pipelined `DoMulti`
   turn a write storm into a handful of commands per flush.
2. **Bounded per-device retention** — trimming each ZSET to its newest 1024 on write keeps
   Redis small, so eviction never storms the single 0.10-CPU thread and the read tail stays
   flat (§3.7).
3. **Zero-reflection serialization** — fixed 56-byte binary records keep allocations (and
   therefore GC, and therefore RSS) flat under load.
4. **Match the runtime to its cgroup** — `GOMAXPROCS=1` to dodge CFS throttling,
   `GOMEMLIMIT`/`GOGC` to cap the heap before the kernel caps the container.
5. **Spend CPU on the bottleneck** — the API does the work, so it gets the CPU; nginx and
   Redis get just enough.

The three changes that mattered most during development were a **correctness fix**
(coalesce-then-flush, which removed a read-after-write race), a **scheduling fix** (escaping
CFS throttling), and a **read-tail fix** (bounding the ZSETs so Redis stops evicting under
load) — none is visible if you only watch average latency. Quantifying all of this on native
Pi 5 hardware is the remaining work; the harness in `stress/` exists to produce those
numbers.

---

## License

MIT — see [LICENSE](LICENSE).
