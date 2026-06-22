# Point 7 — hand-rolled range JSON response (capacity)

## Input
- Diagnostic: at the knee the API tier is co-limiting (~80% of cap) alongside
  Redis. Freeing API CPU raises the knee.
- The range handler (`GET /devices/{id}/telemetry`, 20% of traffic) marshalled up
  to 500 `TelemetryPoint`s per response with reflection-based `encoding/json`.
- (The anomaly-read lever was investigated and dropped: incremental aggregation
  fails — 3 round-robin replicas fragment per-device state, and Redis-side upkeep
  adds cost to the 70% write path to save the 10% anomaly path. Net negative.)

## Output (change)
- `model.TelemetryPoint.AppendJSON(b []byte) []byte` — appends the point's JSON
  with the same field order (ts, lat, lon, [battery], ax, ay, az) and `battery`
  omitempty, using `strconv` (no reflection). Floats use shortest round-trip form
  → values decode identically to `encoding/json` (byte-identity not required;
  no test/contract checks exact float bytes).
- `handler.GetTelemetry` builds `{"points":[...],"next_cursor":...}` into a byte
  buffer and `c.Send`s it (base64 cursors need no escaping). Drops the
  `rangeResponse` struct + `c.JSON`.

## Boundary (NOT this)
- Only the range response path. POST/BATCH/ANOMAYL untouched → their latency
  unchanged; range p99 improves.
- No storage/compose/image-shape change.

## Rules / compliance
- `TestAppendJSONParity`: hand-rolled output decodes to exactly what
  `json.Marshal` produced. Full suite incl. `integration_test` (range + cursor
  pagination) passes `-race` (52 tests).
- Official `test/smoke.js` 28/28 (points[]/cursor checks) + `test/test.js` 0/6051
  locally. Live response verified contract-correct.

## Verify (commit only on measured improvement — met)
- Bench (50-point range): `json.Marshal` 28772 ns/op, 4890 B, 2 allocs →
  **AppendJSON 15314 ns/op, 0 B, 0 allocs** (~1.9×, alloc-free). Deterministic.
- No regression: smoke 28/28, test.js 0 err, per-op p99 unchanged.
- Pi: `test/go` → efficiency/tail held.

## Rollback
Restore `rangeResponse` + `c.JSON(resp)`; remove `AppendJSON`.
