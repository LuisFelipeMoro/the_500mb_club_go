# Point 1 — CPU budget rebalance (capacity)

## Input
- Scoring: capacity = 27% weight, `max_sustained_rps / 1000` clamped 0.25–4.0.
- Field knee: go **1100 RPS** (1.10), tied with rust; zig/cpp 1200.
- Current compose split: `api 3×0.60 + redis 0.10 + lb 0.10 = 2.00`.

## Evidence (why)
- efficiency + tail_latency already clipped at ceiling for all 3 contestants →
  no score room there. Capacity is the headline unwon lever.
- go == rust == 1100 RPS despite rust faster per-request ⇒ bottleneck is
  **shared infra (lb/redis), not API code/language**.
- Local amd64 probe (400 closed-loop workers, single device):
  - api replicas peak 4–11% of their 60% cap → **massively idle**.
  - lb (nginx) pegs its 10% cap; redis 45% of its 10%.
  - Rebalanced to 0.45/0.35/0.30 → delivered **494 → 925 rps (~1.9×)**,
    avg latency 720 → 269 ms. Confirms infra-CPU is the knee.

## Output (change)
docker-compose.yml CPU caps only:
- api-1/2/3: `0.60 → 0.45` (still 4.5× the ~0.10 core each uses at knee;
  < 1.0 so GOMAXPROCS stays 1 — no env change).
- redis: `0.10 → 0.35` (3.5×).
- lb: `0.10 → 0.30` (3×).
- Aggregate: `3×0.45 + 0.35 + 0.30 = 2.00` → within 2.0 CPU cap.
- Memory untouched (296 MiB).

## Boundary (NOT this)
- No image rebuild (compose-only; daemon re-pulls same `:latest`).
- No code change, no nginx.conf change, no mem change.
- nginx-vs-redis exact split is mac-distorted; this is a balanced first cut.
  If Pi shows lb still binding, Point 2 shifts more to lb.

## Rules check
- validate_compose.py: aggregate 2.00 ≤ 2.0 ✓; mem ≤ 500 ✓; ≥3 api / 1 lb /
  1 storage ✓; api hardening (read_only/cap_drop ALL/nnp/non-root) unchanged ✓.

## Verify
- Local: re-probe knee rises (done: ~1.9×).
- Pi: open issue `test/go` → benchmark-done → leaderboard capacity ↑,
  efficiency/tail/resilience/stability not regressed.
- Rollback: revert compose caps to 0.60/0.10/0.10 if capacity drops or any
  dim regresses.
