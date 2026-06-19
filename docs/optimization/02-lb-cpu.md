# Point 2 — more CPU to the LB (capacity)

## Input
- After Point 1 (`api 0.45 / redis 0.35 / lb 0.30`), the LB is still the
  binding constraint.

## Evidence (why)
- In-network k6 probe (container → `lb:80`, bypassing Docker-Desktop vpnkit, so
  CPU readings are trustworthy; open-loop arrival-rate, 50 devices):
  - Sustained avg under load: **lb 23.2% / 0.30 cap (77% util)**, api ~7% / 0.45
    (16%), redis ~7% / 0.35 (19%). APIs absurdly over-provisioned.
  - Fixed-rate knee finder (p99 < 150 ms SLO):
    - **Current** (0.45/0.30/0.35): pass @1000 (9 ms), **fail @1500 (165 ms)** →
      knee ≈ 1400.
    - **Candidate** (0.40/0.45/0.35): pass @2000 (24 ms), @2500 (65 ms),
      **@3000 (78 ms)**, fail @3500 → knee ≈ **3000** (~2×).
  - At 2500 the load finally balances: lb 119% / redis 94% / api 80% of caps —
    no single service grossly starved.

## Output (change)
docker-compose.yml CPU caps only, fed entirely from the idle API tier:
- lb (nginx): `0.30 → 0.45`.
- api-1/2/3: `0.45 → 0.40` (still ~5× the ~0.07 core each uses; < 1.0 →
  GOMAXPROCS stays 1).
- redis: unchanged `0.35`.
- Aggregate: `3×0.40 + 0.45 + 0.35 = 2.00`. Memory untouched (296 MiB).

## Boundary (NOT this)
- No image rebuild, no code/nginx.conf/mem change.
- Local knee is mac-bridged (~2× headroom less than native); the *direction*
  (LB-bound, APIs idle) is trustworthy in-network. On the Pi this moves the knee
  up or is neutral (worst case nginx already cheap → redis/api get the slack).

## Rules check
- validate_compose.py: aggregate 2.00 ≤ 2.0 ✓; mem ≤ 500 ✓; ≥3 api/1 lb/1
  storage ✓; api hardening unchanged ✓.

## Verify
- Local A/B (done): knee ~1400 → ~3000.
- Pi: `test/go` issue confirms efficiency/tail not regressed (caps only affect
  behavior far above the ~200 RPS preview load).
- Rollback: revert lb to 0.30 / api to 0.45 if any dim regresses.
