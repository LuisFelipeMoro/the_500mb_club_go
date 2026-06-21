# Point 5 — zero-copy anomaly read path (tail latency)

## Input
- Leaderboard: Luis anomaly p99 was the worst/most-variable of the field
  (swinging ~2–3.2ms vs competitors' ~1.3ms) — GC jitter on GOMAXPROCS=1.
- The anomaly route fetches the last 256 members from Redis every call.

## Root cause
- `RueidisStore.LastN` did `AsStrSlice()` (256 strings off the reply) and then
  **copied every one into a fresh `[]byte`** (`asMembers`). 256 extra heap
  allocations per anomaly request → GC pressure → p99 tail spikes.

## Output (change)
- `Store.LastN` now returns `[]string` (rueidis-native, no copy). `LastN` is the
  anomaly-only reader, so Range (`[][]byte`) is untouched.
- `model.AccelMagnitudeStr(string)` reads ax/ay/az from the member string via a
  manual little-endian decode (`leUint64`) — zero alloc, **no unsafe**.
- `anomaly.ComputeMembers([]string)` uses it. Same Welford math, same result.

## Boundary (NOT this)
- No contract/response change. Range path, write path, compose, image shape
  untouched.
- No `unsafe`, no new dependency.

## Rules / compliance (100%)
- `ComputeMembers` parity preserved (TestComputeMembersParity vs `Compute`).
- Bench: `ComputeMembers` 0 B/op, 0 allocs/op (was preceded by 256 `[]byte`
  copies in storage — now gone).
- Official `test/smoke.js` 28/28 ✓ incl. anomaly (z_score/samples/anomalous) and
  `samples < 8 -> 404`; `test/test.js` 0/6051 failed — run locally on the stack.
- gofmt/vet clean; 51 tests pass `-race`.

## Verify
- Pi: `test/go` issue — expect anomaly p99 down/steadier, no regression.
- Rollback: restore `LastN ([][]byte)` + `AccelMagnitude([]byte)` + `asMembers`
  for LastN.
