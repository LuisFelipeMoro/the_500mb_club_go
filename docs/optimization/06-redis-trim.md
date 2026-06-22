# Point 6 — probabilistic Redis trim (capacity)

## Input
- Diagnostic (whole-system, through LB): near the knee Redis is the binding tier
  on the Pi (storage-bound; field go==rust==1100). Need more RPS without hurting
  post/batch/anomaly latency.
- Redis `commandstats` at rate 3000 / 20s (baseline, every-flush trim):
  - `zadd`        28310 calls, 529k µs (25%)
  - `zremrangebyrank` 28310 calls, 165k µs (**7.8%**)  ← the trim, runs every flush
  - `zrange`      16980 calls, 1431k µs (67%)          ← reads dominate

## Output (change) — `internal/storage/redis.go` `AddMulti`
- The per-flush `ZREMRANGEBYRANK` is a no-op while a set is ≤ `retainPerDevice`
  (1024) yet still a pipelined command on the 70%-write hot path.
- Trim **probabilistically per device** (`rand.IntN(trimDivisor)==0`, `trimDivisor=8`)
  instead of every flush → ~7/8 fewer trim commands → frees ~7% of Redis command
  CPU. `math/rand/v2` (not crypto): pure load-shaping, not security-sensitive,
  and crypto/rand's syscall+alloc would burn the CPU this lever frees.
- **Probabilistic, not a global counter:** every device has a 1/8 chance each
  flush, so no hot device is starved of trimming (a global counter could skip a
  device forever → unbounded set → eviction).

## Boundary (NOT this)
- No change to the request path (writer is async) → post/batch/anomaly latency
  untouched; anomaly/range p99 *improve* slightly under load (less Redis
  contention). Reads (`LastN`/`Range`) untouched.
- Reads stay correct on a slightly larger set; expected size ~1024 + a few
  flushes' adds, far under maxmemory 50mb → RSS/stability unaffected.

## Rules / compliance
- Redis result semantics identical (set still bounded; reads correct).
- Must pass: gofmt/vet, `go test -race ./...`, official `test/smoke.js` (28/28),
  `test/test.js` (0 err). Compose/image shape unchanged.

## Verify (commit only on measured improvement)
- A/B `commandstats` same load: `zremrangebyrank` calls drop ~8× vs `zadd`
  (≈3.5k vs 28k), `zadd`/`zrange` unchanged → ~7% less Redis command CPU.
- No regression: smoke 28/28, per-op p99 (post/batch/anomaly) not worse, redis
  mem flat.
- Pi: `test/go` issue → efficiency/tail held.

## Note
Reads (zrange, 67% of Redis CPU) are the larger target — a future lever on the
anomaly 256-member fetch would beat this. Lever 1 is the smaller, strictly
latency-safe win requested.
