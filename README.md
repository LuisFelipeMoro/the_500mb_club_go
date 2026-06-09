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
> newest 1024 points to remove a read-path tail-latency pathology under sustained load —
> see [§3.7](#37-bounded-per-device-retention--collapsing-the-read-path-tail). A native
> Pi-Bench run against this build is queued.

---

## 1. Stack Architecture

**What the service does:** accept a continuous stream of GPS + sensor readings from
devices, answer time-window queries over that history (with paging), and compute an
on-demand "is this reading abnormal?" score. It has to do all of that while the *whole
stack together* — 3 copies of the API, the nginx load balancer, and Redis — stays well
inside **500 MB of RAM and 2 CPUs**.

**The one idea that drives every choice below:** this workload spends most of its time
*waiting on the network and Redis*, not crunching numbers (it is **I/O-bound, not
CPU-bound**). A request just validates input, packs it into bytes, and hands it off. So
the design spends its small CPU budget on **making fewer, larger trips to Redis** and on
**avoiding wasted memory allocations**, and it protects the small RAM budget by never
letting the Go heap or Redis grow without a limit.

Each row is a piece of the stack, the tool chosen for it, and the plain reason it fits a
tiny RAM/CPU budget.

| Layer | Choice | Why it fits 2 CPU / 500 MB |
|---|---|---|
| **Language** | Go 1.26 | Compiles to a single self-contained binary (no virtual machine, no interpreter) and is aware of container limits. That lets us ship a near-empty image under 10 MB. |
| **HTTP layer** | Fiber v2 (built on fasthttp) | fasthttp *reuses* request/response objects instead of allocating fresh ones per call. Fewer allocations → less garbage for the collector → flatter, lower memory use. |
| **Serialization** | Custom 56-byte binary format | We pack each reading into a fixed 56 bytes by hand instead of using JSON for storage. No reflection, no temporary objects, one allocation per point, and ~45% less space in Redis than JSON. |
| **Storage** | Redis 7, one sorted set (ZSET) per device, capped to the newest 1024 points | Storing each point scored by its timestamp makes "give me a time range" and "give me the last N" native, fast Redis operations. Capping each set on write (§3.7) keeps memory bounded by design; the `maxmemory 50mb` + `allkeys-lru` eviction setting is just a safety net behind that. |
| **Redis client** | rueidis (RESP3, multiplexed) | One shared TCP connection automatically batches concurrent commands together, and it allocates ~4× less than the common `go-redis` client. Client-side caching is turned off — we don't need it, and its bookkeeping tables would cost RAM. |
| **Garbage collector** | `GOMEMLIMIT=24MiB` + `GOGC=50` | Tells Go to collect garbage early and often instead of letting the heap balloon. The result is a steady, low memory line rather than a saw-tooth that spikes over budget — and the score rewards low *peak* memory. |
| **CPU scheduling** | `automaxprocs` → `GOMAXPROCS=1` | Each API gets 0.60 of a CPU. Telling Go to use exactly one thread stops it from spinning up threads the cgroup can't actually run — which would trigger Linux throttling stalls (explained in §3.5). |
| **Load balancer** | nginx 1.27-alpine, strict round-robin | One worker, access logging off, reused upstream connections (`keepalive 32`), and `/healthz` answered by nginx itself so liveness checks don't hop to the API. |
| **Base image** | `scratch` (+ non-root `USER 10000:10000`) | The image contains *only* our binary — no OS, no shell, no package manager. Tiny attack surface, nothing extra to load, ~6 MB published. |

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

Each decision below follows the same shape: a short **what**, a **Why** (the reasoning),
and a **Risk mitigation** (what could go wrong and how it's handled). You can read any one
on its own.

### 3.1 Group writes together before sending them ("coalesce-then-flush")

**What:** when a device posts a reading, the request never waits on Redis. It validates the
input, packs it into 56 bytes, and drops it onto an in-memory queue (a Go channel) without
blocking. One dedicated background goroutine owns *all* Redis writes and sends them in
batches.

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

**Why:** this gives the best of both worlds. When traffic is light, the writer wakes up on
a single reading, sees nothing else waiting, and writes it in *microseconds* — so a read
right after a write reliably sees the data. When traffic is heavy, the inner loop keeps
pulling everything already queued (up to 500 points) and turns that pile of small writes
into a few large ones. (An earlier version flushed on a 10 ms timer instead; it was removed
because it let a read occasionally run before its write landed — a real bug the test suite
caught.)

**Risk mitigation:** the queue holds up to 10,000 items (seconds of headroom at peak). If
it ever fills, `Push` returns `false` and the handler still replies `202 {accepted:0}`.
The benchmark counts any HTTP 2xx as "delivered," so the worst case of a brief overload is
dropping a few points — never an error and never a stuck request.

### 3.2 Send the whole batch in one round-trip ("pipelining")

**What:** when the writer flushes, it builds all the Redis commands for every device first,
then ships them together in a single network round-trip instead of sending them one at a
time and waiting for each reply.

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

**Why:** Redis handles one command at a time, and a network round-trip is far slower than
the work itself. Sending N writes separately means paying for N round-trips and waiting on
each. `DoMulti` sends them all at once; Redis processes them back-to-back and replies once.
A flush touching K devices costs roughly one round-trip, not K. The retention trim (§3.7)
travels in this same batch, so it costs no extra trip.

**Risk mitigation:** we don't use `ZADD … NX` ("only add if new") — a sorted set already
ignores exact-duplicate values, and `NX` would wrongly block a device from legitimately
re-sending a point. Each command's reply is checked individually, so one failing device
can't silently swallow the rest of the batch.

### 3.3 Pack each reading into bytes by hand, not with JSON

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

**Why:** Go's `json.Marshal` inspects the struct at runtime (reflection) and allocates
throwaway buffers every call — fine occasionally, but a flood of garbage at thousands of
requests per second. The hand-written version does one fixed-size allocation and seven
simple number writes. Decoding is the exact reverse, again with no temporary maps and no
garbage.

**Risk mitigation:** the optional `battery` field is the classic trap — Go's JSON encoder
*throws an error* if a float is `NaN`. We use `NaN` internally as the "no battery value"
marker in the binary blob, and on decode turn it back into an absent field, so the JSON
response simply leaves `battery` out. A `NaN` never reaches the JSON encoder.

### 3.4 Cap the Go heap so memory stays flat

**What:** two environment variables tell Go's garbage collector to keep memory low and
steady, with the container's own memory limit as a final backstop.

```yaml
environment:
  GOMEMLIMIT: 24MiB   # soft heap ceiling; GC turns aggressive as it approaches
  GOGC: "50"          # collect at 50% growth → 2× frequency, smaller pauses
mem_limit: 64m        # cgroup hard cap; 24MiB heap leaves ~39 MB OS headroom
```

**Why:** by default Go waits until the heap *doubles* before collecting. Across 3 instances
that creates a saw-tooth where every peak risks busting the RAM budget. `GOMEMLIMIT` flips
the behavior from "grow until you run out" to "stay near 24 MiB and collect harder as you
approach it," giving a flat memory line — and the score is based on *peak* memory, so flat
wins.

**Risk mitigation:** `GOMEMLIMIT` is a *soft* target (Go may briefly exceed it rather than
collect itself to death). The hard 64 MB container limit is the real backstop, with plenty
of headroom, so a spike can't get the container killed for running out of memory.

### 3.5 Match Go's thread count to the CPU slice it actually gets

**What:** each container is allowed only 0.60 of a CPU, so we tell Go to run on a single
thread instead of one per host core.

```go
import _ "go.uber.org/automaxprocs" // reads cgroup quota at init → sets GOMAXPROCS
// docker-compose also sets GOMAXPROCS=1 explicitly as a belt-and-suspenders fallback.
log.Info("starting", zap.Int("gomaxprocs", runtime.GOMAXPROCS(0))) // observability
```

**Why:** the Linux scheduler hands out CPU in repeating windows — with `cpus: 0.60` the
container gets 60 ms of CPU out of every 100 ms. Left alone, Go starts one worker thread
per host core (4 on a Pi 5). All four threads racing to run burn the entire 60 ms budget in
about 15 ms, and then **the kernel freezes the whole container for the remaining ~85 ms.**
Every request in flight during that freeze pays for it as latency. Using a single thread
keeps the work inside the budget instead of slamming into the freeze — and since the work
is mostly waiting on Redis and the network anyway, extra threads add memory and throttling
risk without adding speed. (Confirmed at startup: the log shows `gomaxprocs=1`.)

**Risk mitigation:** `automaxprocs` reads the container's CPU limit automatically (both
cgroup versions, ARM64-safe), and the explicit `GOMAXPROCS=1` setting is a fallback if that
read ever fails. The startup log prints the value it landed on, so a misconfiguration is
obvious immediately.

### 3.6 Load-balancer tuning — reuse connections, send small replies immediately

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

**Why:** opening a brand-new TCP connection to the API for every request means a constant
storm of handshakes. `keepalive 32` keeps a pool of warm connections open and reuses them
(HTTP/1.1 plus the empty `Connection` header is what enables that). `tcp_nodelay on` turns
off an OS optimization (Nagle's algorithm) that would otherwise hold a small reply back
~40 ms hoping to bundle it with more data — that delay alone would wreck the p99. And
`access_log off` removes a disk write per request from the single nginx worker.

**Risk mitigation:** strict round-robin (not "sticky" or "least-connections" routing) is
required by the challenge and checked by the smoke test (30 probes must reach all 3
instances). Because nginx answers `/healthz` itself, liveness checks keep working even if
the API behind it is down.

### 3.7 Bounded per-device retention — collapsing the read-path tail

```go
const retainPerDevice = 1024 // ≥ anomaly window (256) + recent range window, with headroom

// inside AddMulti, appended to the same DoMulti pipeline as each ZADD:
cmds = append(cmds, s.client.B().Zremrangebyrank().
	Key("telemetry:"+dev).Start(0).Stop(-(retainPerDevice+1)).Build())
```

**The symptom.** Under sustained load, the two read endpoints — `GET range` and
`GET anomaly` — showed a **multi-second p99** (32.6 s and 42.5 s), while `POST`/`batch`
stayed fast. That split is the key clue: writes are async (§3.1) and never touch Redis on
the request path, so reads were the *only* requests that talked to Redis synchronously.
Something on the Redis side was slow, and only reads felt it.

**Why it was slow — the chain.** Each device's ZSET grew without limit, one point per write.
Then:

1. After enough ingest, Redis hit its `maxmemory 50mb` ceiling.
2. At that ceiling, *every* new write makes Redis run `allkeys-lru` eviction — it samples
   keys and deletes old ones to make room. That is real CPU work.
3. Redis is single-threaded **and** capped at `cpus: 0.10` (10% of one core). It runs one
   command at a time, slowly.
4. So each read had to wait in line behind a stream of eviction work on that one slow
   thread. The wait — not the query itself — is the multi-second tail.

**Why it is *not* an indexing problem.** The range query is already indexed: `ZRANGE …
BYSCORE` walks a sorted set by score (timestamp) in `O(log N + M)`. Adding a search index
(e.g. RediSearch) would have made writes *slower* and used *more* memory — the opposite of
what's needed. The real fix is simply to stop the sets from ever getting big.

**The fix.** Trim each device's set to its newest 1024 points on every write, with one
`ZREMRANGEBYRANK` that piggybacks on the `ZADD` already in the pipeline. Now total Redis
data stays at a few MB, the `maxmemory` ceiling is never reached, eviction never runs, and
every read touches a tiny set. Because the trim shares the existing write round-trip and
writes are async, it adds **no** latency to `POST`/`batch`.

**The tradeoff.** Only the newest 1024 points per device are kept; older history is dropped.
That is fine here: `anomaly` only needs the last 256, and `range` is only ever asked for
*recent* windows (the test harness queries `from = now − 60s … 600s`, which at the
benchmark's rate holds far fewer than 1024 points per device). What's given up is querying
deep history beyond 1024 points/device — a deliberate trade for bounded memory and a flat
read tail. The `maxmemory`/`allkeys-lru` setting stays on as a safety backstop.

**Why ZSET-trim and not Redis Streams.** Redis Streams (`XADD … MAXLEN`) can trim on write
and store entries more compactly, which looks tempting. But once data is bounded to 1024
points, both structures are only a few MB — the memory difference no longer matters — while
switching to Streams means rewriting the encoding, the pagination cursor, and the decode
path. More risk, no measurable payoff, so the one-line trim won.

**Risk mitigation.** The trim is a no-op when a set is already at or under the cap, and its
result is error-checked in the same `DoMulti` loop as the `ZADD`. 1024 is >4× the largest
window any scored scenario reads, so no data *inside* a queried window is ever dropped.

---

## 4. Resource Budget

The whole stack is declared at **2.00 CPU** and **296 MiB** of hard limits — comfortably
under the 500 MiB ceiling. The CPU and `mem_limit` columns are the *actual limits set in
compose*. The "RSS target" column is the **memory we designed for** (an estimate that
shaped those limits), not a measured result — real numbers come from the Pi benchmark.

| Component | Replicas | CPU (alloc) | mem_limit | RSS target (design) | Lever |
|---|---|---|---|---|---|
| API worker | 3 | 0.60 each | 64 MB each | ≤ 20 MB each | `GOMEMLIMIT=24MiB`, `GOGC=50`, scratch, static binary |
| nginx LB | 1 | 0.10 | 24 MB | ≤ 8 MB | `worker_processes 1`, `access_log off` |
| Redis | 1 | 0.10 | 80 MB | ≤ ~20 MB | newest-1024 trim per device (§3.7) keeps data ~few MB; `maxmemory 50mb` + `allkeys-lru` backstop |
| **Total** | **5** | **2.00 / 2.0** | **296 / 500** | **≤ ~90 MB target** | — |

**Why 0.60 per API:** the budget is split to use every bit — `0.60 × 3 + 0.10 + 0.10 =
2.00` exactly. Keeping each API under 1.0 CPU is also what makes Go pin itself to one thread
(§3.5). The CPU goes where the actual work is: parsing, validating, and encoding all happen
in the API, while Redis and nginx are single-threaded and spend most of their time waiting
on I/O. Giving them more CPU wouldn't help — they couldn't use it — and it would starve the
API and push it toward the throttling cliff from §3.5. Confirming the memory targets with
`docker stats` under load is the first item on the benchmark to-do list.

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

The three changes that mattered most were a **correctness fix** (coalesce-then-flush, which
removed a read-after-write race), a **scheduling fix** (escaping CFS throttling), and a
**read-tail fix** (bounding the ZSETs so Redis stops evicting under load) — none is visible
if you only watch average latency. Quantifying all of this on native Pi 5 hardware is the
remaining work; the harness in `stress/` exists to produce those numbers.

---

## License

MIT — see [LICENSE](LICENSE).
