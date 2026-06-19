# Point 3 — nginx LB throughput tuning (capacity)

## Input
- After Points 1–2 (`api 0.40 / lb 0.45 / redis 0.35`) the nginx LB is still the
  top CPU consumer at the knee (in-network: lb pegs its cap while api/redis trail).

## Evidence (why)
- In-network k6 fixed-rate knee finder (container → `lb:80`, p99 < 150 ms):
  - **Current nginx.conf** (P2 caps): pass @3000 (p99 78 ms), fail @3500 → knee ≈ 3000.
  - **Tuned nginx.conf** (same caps): @3000 p99 **12 ms** (6× lower), @3300 13 ms,
    @3600 35 ms, **@4000 125 ms** (delivered 99.8%) → knee ≈ **4000**.
- Resilience: with the knee now ~4000, the spike peak (challenge 800 RPS) is
  trivial — local spike overall p99 ~1.7 ms, 0 errors → resilience clips at 2.0.

## Output (change) — nginx.conf only
- `proxy_buffering off` on `/`: stream the small JSON responses straight through
  instead of buffering — less per-request copy/CPU on the bottleneck LB.
- upstream `keepalive 32 → 64`: more warm upstream conns at peak.
- `keepalive_requests 100000` (client + upstream) + upstream `keepalive_timeout
  60s`: stop recycling connections mid-load (recycle = handshake CPU on the LB).

## Boundary (NOT this)
- No image, code, compose-cap, or memory change.
- No gate validates nginx.conf content; nginx still boots and serves (readyz 200,
  full traffic verified locally).
- Local knee is mac-bridged; direction (LB-bound) is trustworthy in-network.

## Rules check
- Compose unchanged → validate_compose still 0 FAIL/0 WARN.
- LB hardening (read_only, no-new-privileges, tmpfs) untouched.

## Verify
- Local A/B (done): knee ~3000 → ~4000; p99 at 3000 cut 6×.
- Pi: `test/go` issue confirms efficiency/tail not regressed.
- Rollback: restore previous nginx.conf (keepalive 32, no proxy_buffering line).
