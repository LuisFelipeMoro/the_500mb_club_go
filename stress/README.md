# Stress harness

Self-contained k6 load tests for the Pi-Bench stack. No remote imports and no
dependency on the challenge repo, so it runs inside the `internal: true` docker
network (no egress required).

## Scenarios

| Scenario | Profile | Validates |
|---|---|---|
| `steady` | 50 VUs, 1 min | steady-state per-op p99 vs SLO |
| `spike` | 200 → 2000 → 200 RPS | channel absorption, error rate < 1% |
| `capacity` | 1k → 12k arrival-rate ramp | sustained RPS + saturation knee |
| `maxrps` | 5k → 40k arrival-rate ramp | push to the ceiling |
| `endurance` | 200 VUs, 30 min | latency / RSS drift over time |
| `rate` | fixed `RATE` for `DURATION` | hold one rate (used by `score.sh`) |

The traffic mix mirrors the challenge steady profile: `post 60 / batch 10 /
range 20 / anomaly 10`. `setup()` pre-seeds every device with 8 points so anomaly
never 404s on a cold device.

## Running

First bring the stack up (from the `implementation` branch):

```bash
git switch implementation && docker compose up -d   # or: docker-compose up -d
```

Then, from the `main` branch checkout:

```bash
stress/run.sh steady
stress/run.sh spike
stress/run.sh capacity
stress/run.sh endurance
```

### Modes

- **Container mode (default).** Runs k6 in a `grafana/k6` container attached to the
  stack's docker network, hitting `http://lb:80`. This is required on macOS and on any
  `internal: true` network, where the LB's published host port is **not** reachable
  (Docker does not bind host ports for containers on an internal-only network).
- **Host mode.** `K6_MODE=host stress/run.sh steady` runs a locally-installed `k6`
  against `BASE_URL` (default `http://localhost:8080`). Use this on a native Linux /
  arm64 host where the LB port is published and reachable.

Overrides: `NET=<network>` to pick the docker network, `BASE_URL=<url>`,
`DEVICE_COUNT=<n>`.

## Reading results

`stress.js` tags each request by `op`, so k6 reports per-endpoint percentiles:

```
http_req_duration{op:post}.....: p(99)=...
http_req_duration{op:batch}....: p(99)=...
http_req_duration{op:range}....: p(99)=...
http_req_duration{op:anomaly}..: p(99)=...
http_req_failed................: rate=...
```

The SLO thresholds in `stress.js` (`post<8ms`, `batch<25ms`, `range<15ms`,
`anomaly<25ms`, `failed<0.5%`) are tuned for **native arm64**. Under QEMU emulation
(e.g. an x86 dev box) they will not hold — there, a run only proves the harness and the
stack wiring, not real latency.

## Score estimate

The challenge's exact global-score formula (see `docs/en/scoring.md`) is implemented
**in JavaScript inside `stress.js`** (`handleSummary`): five weighted dimensions,
per-metric clipped ratios, and the "missing metric" renormalization so an un-measured
dimension is excluded rather than penalised. **Every k6 run prints a score** at the end —
no Python, no extra tooling.

What each run can compute on its own:
- **tail_latency** — per-op p99 (from `steady`/`rate` runs).
- **resilience** — spike p99 + error (from the `spike` run).

What must be fed via `--env`, because k6 cannot read it from inside the test:
- **efficiency** — `RSS_MB` + `CPU_PCT` (aggregate container RSS/CPU; k6 has no view of
  the host/containers).
- **capacity** — `MAX_RPS` (the SLO-gated knee from a real `capacity` ramp; a fixed-rate
  run is not a capacity measurement).
- **stability** — `LAT_DRIFT` + `RSS_DRIFT` (from the 30-min `endurance` run).

```bash
# Direct: pass everything you have; missing dims are excluded + renormalized.
k6 run --env BASE_URL=http://localhost:8080 \
  --env RSS_MB=122 --env CPU_PCT=18 \
  --env MAX_RPS=1800 --env SPIKE_P99=9 --env SPIKE_ERR=0 \
  --env LAT_DRIFT=1.02 --env RSS_DRIFT=1.01 \
  stress.js
```

`score.sh` is a thin convenience wrapper that does the **one** thing k6 can't — samples
aggregate container RSS/CPU with `docker stats` — then runs `stress.js` with those values
so the efficiency dimension is included automatically:

```bash
stress/score.sh                                   # efficiency + tail_latency + gate
MAX_RPS=1800 SPIKE_P99=9 SPIKE_ERR=0 stress/score.sh   # fold in more dims via env
```

> **Valid environment only.** The summary warns if steady p99 exceeds ~200 ms, which means
> the host (Docker Desktop on macOS, or QEMU emulation) adds so much overhead that
> latency/capacity are meaningless — trust only RSS there. Run on a native arm64 host (the
> Pi 5) for a real tail_latency and capacity score.
