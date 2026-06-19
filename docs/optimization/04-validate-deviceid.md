# Point 4 — hand-rolled DeviceID validation (tail latency)

## Input
- `validate.DeviceID` ran a `regexp.MustCompile(...).MatchString` on EVERY
  request (post/batch/range/anomaly call it first).
- Leaderboard preview: Luis p99 post 1.99ms / anomaly 3.22ms, and the per-op
  p99s swing 1.6–3.2ms run-to-run vs competitors' steadier ~1.3ms — symptom of
  per-request allocation/GC jitter on GOMAXPROCS=1.

## Output (change)
- Replace the regexp with a hand-rolled byte scan of `^[a-zA-Z0-9_-]{1,64}$`.
  Zero-alloc, no regexp engine, runs in tens of ns instead of hundreds.
- `internal/validate/validate.go` only; drop the `regexp` import.

## Boundary (NOT this)
- Behavior is byte-for-byte identical to the regexp (proven, below).
- No contract/route/response change. No other file touched.

## Rules / compliance (100%)
- OpenAPI `openapi.yaml` device-id pattern IS `^[a-zA-Z0-9_-]{1,64}$` — the scan
  matches it exactly.
- `FuzzDeviceID` pins the scan to the old regexp over arbitrary input (passed).
- Official challenge tests run locally against the stack:
  - `test/smoke.js`: 28/28 checks ✓, `http_req_failed 0.00%` (incl. every
    400/413/404 validation).
  - `test/test.js`: `http_req_failed 0.00%` (0 of 6051).
- Image unchanged in shape (scratch, non-root, arm64) → audit_image/compose
  gates unaffected.

## Verify
- Local: gofmt/vet clean, 51 tests + fuzz pass `-race`; steady + rate runs 0
  errors, p99 unchanged-or-better.
- Pi: `test/go` issue — expect post p99 down/steadier, no regression elsewhere.
- Rollback: restore the regexp DeviceID.
