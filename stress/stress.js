// Self-contained k6 stress + scoring script for the Pi-Bench stack.
//
// No remote imports and no external dependency, so it runs inside the
// `internal: true` docker network (no egress). handleSummary() computes the
// challenge's global score (docs/en/scoring.md) in pure JS and prints it at the
// end of every run — no Python, no extra tooling.
//
// Scenario via SCENARIO env (see stress/run.sh):
//   steady     constant 50 VUs, 1 min        — per-op p99 vs SLO + tail_latency dim
//   rate       fixed RATE for DURATION        — hold a rate; feeds efficiency/tail/capacity
//   spike      arrival-rate 200→2000→200      — resilience dim (spike p99 + error)
//   capacity   arrival-rate ramp 1k→12k       — saturation curve
//   maxrps     arrival-rate ramp 5k→40k       — push to the ceiling
//   endurance  constant 200 VUs, 30 min       — stability inputs
//
// k6 cannot read container RSS/CPU, so the efficiency dimension is fed via env:
//   --env RSS_MB=122 --env CPU_PCT=18      (sample with `docker stats`; stress/run.sh does it)
// Likewise fold in dims a single run can't produce:
//   --env MAX_RPS=1800 --env SPIKE_P99=9 --env SPIKE_ERR=0 --env LAT_DRIFT=1.02 --env RSS_DRIFT=1.01
// Any dimension with no metrics is excluded and the weights renormalize
// (challenge "missing metric" policy) — you are never penalised for what you didn't measure.

import http from 'k6/http';

const BASE = __ENV.BASE_URL || 'http://lb:80';
const DEVICE_COUNT = parseInt(__ENV.DEVICE_COUNT || '50', 10);
const JSON_HEADER = { 'Content-Type': 'application/json' };
const SCENARIO = __ENV.SCENARIO || 'steady';

const SCENARIOS = {
  steady: { executor: 'constant-vus', vus: 50, duration: '1m' },
  rate: {
    executor: 'constant-arrival-rate',
    rate: parseInt(__ENV.RATE || '2000', 10), timeUnit: '1s',
    duration: __ENV.DURATION || '20s', preAllocatedVUs: 600, maxVUs: 10000,
  },
  spike: {
    executor: 'ramping-arrival-rate', startRate: 200, timeUnit: '1s',
    preAllocatedVUs: 300, maxVUs: 4000,
    stages: [{ target: 200, duration: '30s' }, { target: 2000, duration: '5s' }, { target: 200, duration: '30s' }],
  },
  capacity: {
    executor: 'ramping-arrival-rate', startRate: 500, timeUnit: '1s',
    preAllocatedVUs: 400, maxVUs: 6000,
    stages: [
      { target: 1000, duration: '20s' }, { target: 2000, duration: '20s' },
      { target: 3000, duration: '20s' }, { target: 5000, duration: '20s' },
      { target: 8000, duration: '20s' }, { target: 12000, duration: '20s' },
    ],
  },
  maxrps: {
    executor: 'ramping-arrival-rate', startRate: 2000, timeUnit: '1s',
    preAllocatedVUs: 800, maxVUs: 12000,
    stages: [
      { target: 5000, duration: '20s' }, { target: 10000, duration: '20s' },
      { target: 20000, duration: '20s' }, { target: 40000, duration: '20s' },
    ],
  },
  endurance: { executor: 'constant-vus', vus: 200, duration: '30m' },
};

export const options = {
  scenarios: { run: SCENARIOS[SCENARIO] },
  summaryTrendStats: ['avg', 'min', 'med', 'p(95)', 'p(99)', 'max'], // ensure p(99) in handleSummary
  thresholds: {
    http_req_failed: ['rate<0.05'],
    'http_req_duration{op:post}': ['p(99)<8'],
    'http_req_duration{op:batch}': ['p(99)<25'],
    'http_req_duration{op:range}': ['p(99)<15'],
    'http_req_duration{op:anomaly}': ['p(99)<25'],
  },
};

function ri(min, max) { return Math.floor(Math.random() * (max - min + 1)) + min; }
function deviceId() { return `dev-${ri(1, DEVICE_COUNT)}`; }

function point(tsOffset) {
  return {
    ts: Date.now() - (tsOffset || 0),
    lat: -23.55 + (Math.random() - 0.5) * 0.1,
    lon: -46.63 + (Math.random() - 0.5) * 0.1,
    battery: Math.random(),
    ax: (Math.random() - 0.5) * 4, ay: (Math.random() - 0.5) * 4, az: 9.8 + (Math.random() - 0.5) * 2,
  };
}
function batch(n) {
  const points = new Array(n);
  for (let i = 0; i < n; i++) points[i] = point((n - 1 - i) * 100);
  return JSON.stringify({ points });
}

export function setup() {
  for (let i = 1; i <= DEVICE_COUNT; i++) {
    http.post(`${BASE}/devices/dev-${i}/telemetry/batch`, batch(8), { headers: JSON_HEADER });
  }
}

const MIX = { post: 0.60, batch: 0.10, range: 0.20, anomaly: 0.10 };
function pickOp(rand) {
  let acc = 0;
  for (const op of Object.keys(MIX)) { acc += MIX[op]; if (rand < acc) return op; }
  return 'post';
}

export default function () {
  const dev = deviceId();
  switch (pickOp(Math.random())) {
    case 'post':
      http.post(`${BASE}/devices/${dev}/telemetry`, JSON.stringify(point(0)),
        { headers: JSON_HEADER, tags: { op: 'post' } });
      break;
    case 'batch':
      http.post(`${BASE}/devices/${dev}/telemetry/batch`, batch(ri(10, 50)),
        { headers: JSON_HEADER, tags: { op: 'batch' } });
      break;
    case 'range': {
      const now = Date.now();
      http.get(`${BASE}/devices/${dev}/telemetry?from=${now - 600000}&to=${now}&limit=100`,
        { tags: { op: 'range' } });
      break;
    }
    case 'anomaly':
      http.get(`${BASE}/devices/${dev}/anomaly`, { tags: { op: 'anomaly' } });
      break;
  }
}

// ─────────────────────────── scoring (challenge docs/en/scoring.md) ───────────────────────────

const WEIGHTS = { efficiency: 0.32, capacity: 0.27, tail_latency: 0.20, resilience: 0.13, stability: 0.08 };

function clip(x, lo, hi) { return Math.max(lo, Math.min(hi, x)); }
function scoreRss(rssMb) { return clip(0.25 + 3.75 * (500 - rssMb) / 450, 0.25, 4.0); }
function mean(xs) { return xs.reduce((a, b) => a + b, 0) / xs.length; }
function envNum(name) {
  const s = __ENV[name];
  if (s === undefined || s === null || s === '') return null;
  const n = Number(s); return isNaN(n) ? null : n;
}

function computeDims(m) {
  const dims = {};
  const eff = [];
  if (m.rss_mb !== null) eff.push(scoreRss(m.rss_mb));
  if (m.cpu_pct !== null && m.cpu_pct > 0) eff.push(clip(40 / m.cpu_pct, 0.25, 4.0));
  if (eff.length) dims.efficiency = mean(eff);

  if (m.max_rps !== null) dims.capacity = clip(m.max_rps / 1000, 0.25, 4.0);

  const tail = [];
  const tgt = { p99_post: 8, p99_batch: 25, p99_range: 15, p99_anomaly: 25 };
  for (const k in tgt) if (m[k] !== null && m[k] > 0) tail.push(clip(tgt[k] / m[k], 0.25, 1.5));
  if (tail.length) dims.tail_latency = mean(tail);

  const res = [];
  if (m.spike_p99 !== null && m.spike_p99 > 0) res.push(clip(12 / m.spike_p99, 0.25, 2.0));
  if (m.spike_err !== null) res.push(m.spike_err <= 0 ? 2.0 : clip(1 / m.spike_err, 0.25, 2.0));
  if (res.length) dims.resilience = mean(res);

  const stab = [];
  if (m.lat_drift !== null && m.lat_drift > 0) stab.push(clip(1.10 / m.lat_drift, 0.25, 1.5));
  if (m.rss_drift !== null && m.rss_drift > 0) stab.push(clip(1.10 / m.rss_drift, 0.25, 1.5));
  if (stab.length) dims.stability = mean(stab);

  return dims;
}

function val(data, name, stat) {
  const m = data.metrics[name];
  if (m && m.values && m.values[stat] !== undefined) return m.values[stat];
  return null;
}

export function handleSummary(data) {
  const errRate = val(data, 'http_req_failed', 'rate');
  const errPct = (errRate === null ? 0 : errRate) * 100;
  const rps = val(data, 'http_reqs', 'rate');
  const overallP99 = val(data, 'http_req_duration', 'p(99)');
  const p99 = (op) => val(data, `http_req_duration{op:${op}}`, 'p(99)');

  const m = {
    rss_mb: envNum('RSS_MB'), cpu_pct: envNum('CPU_PCT'),
    p99_post: null, p99_batch: null, p99_range: null, p99_anomaly: null,
    max_rps: null, spike_p99: null, spike_err: null, lat_drift: envNum('LAT_DRIFT'), rss_drift: envNum('RSS_DRIFT'),
  };

  // tail_latency: only from the steady-like measurement (steady/rate), per the challenge.
  if (SCENARIO === 'steady' || SCENARIO === 'rate') {
    m.p99_post = p99('post'); m.p99_batch = p99('batch');
    m.p99_range = p99('range'); m.p99_anomaly = p99('anomaly');
  }

  // capacity: only from an explicit MAX_RPS (the SLO-gated knee from a real ramp
  // on the Pi). A fixed-rate steady run is NOT a capacity measurement — deriving
  // it from the held rate would wrongly floor the dimension, so it is excluded
  // unless MAX_RPS is supplied.
  if (envNum('MAX_RPS') !== null) m.max_rps = envNum('MAX_RPS');

  // resilience: spike scenario measures it directly; else from env.
  if (SCENARIO === 'spike') { m.spike_p99 = overallP99; m.spike_err = errPct; }
  else { m.spike_p99 = envNum('SPIKE_P99'); m.spike_err = envNum('SPIKE_ERR'); }

  const dims = computeDims(m);
  const wsum = Object.keys(dims).reduce((a, d) => a + WEIGHTS[d], 0);
  const score = wsum ? 100 * Object.keys(dims).reduce((a, d) => a + WEIGHTS[d] * dims[d], 0) / wsum : 0;

  const ms = (x) => x === null ? '?' : x.toFixed(1);
  let out = '\n=== 500MB Club score estimate ===\n\n';
  out += `scenario=${SCENARIO}  requests=${data.metrics.http_reqs ? data.metrics.http_reqs.values.count : '?'}  `;
  out += `rps=${ms(rps)}  error=${errPct.toFixed(2)}%  overall_p99=${ms(overallP99)}ms\n`;
  if (SCENARIO === 'steady' || SCENARIO === 'rate') {
    out += `p99 ms: post=${ms(m.p99_post)} batch=${ms(m.p99_batch)} range=${ms(m.p99_range)} anomaly=${ms(m.p99_anomaly)}\n`;
  }
  out += `efficiency inputs: rss=${m.rss_mb === null ? 'unset' : m.rss_mb + 'MB'} cpu=${m.cpu_pct === null ? 'unset' : m.cpu_pct + '%'}\n\n`;

  // gate
  const gate = [];
  gate.push(['error<0.5%', errPct < 0.5]);
  if (m.rss_mb !== null) gate.push(['rss<500MB', m.rss_mb < 500]);
  if (m.cpu_pct !== null) gate.push(['cpu<200%', m.cpu_pct < 200]);
  out += 'gate: ' + (gate.every((g) => g[1]) ? 'PASS' : 'FAIL (gated → off podium)') + '\n';
  for (const g of gate) out += `  [${g[1] ? 'x' : ' '}] ${g[0]}\n`;
  out += '\n';

  out += 'dimension      weight   value   contrib\n';
  for (const d in WEIGHTS) {
    if (dims[d] !== undefined) {
      const contrib = 100 * WEIGHTS[d] * dims[d] / wsum;
      out += `${d.padEnd(14)} ${WEIGHTS[d].toFixed(2).padStart(5)} ${dims[d].toFixed(3).padStart(7)} ${contrib.toFixed(1).padStart(9)}\n`;
    } else {
      out += `${d.padEnd(14)} ${WEIGHTS[d].toFixed(2).padStart(5)} ${'—'.padStart(7)} ${'excluded'.padStart(9)}\n`;
    }
  }
  const missing = Object.keys(WEIGHTS).filter((d) => dims[d] === undefined);
  out += '\n';
  if (missing.length) out += `NOTE: excluded (not measured): ${missing.join(', ')} — weights renormalized.\n`;
  if (m.p99_post !== null && m.p99_post > 200) {
    out += '!! WARNING: p99 >200ms at low load — host is NOT a valid latency/capacity bench\n';
    out += '!!          (Docker Desktop / QEMU overhead). Trust RSS; run on native arm64 (Pi 5).\n';
  }
  out += `\nESTIMATED GLOBAL SCORE: ${score.toFixed(1)}   (100 = meets all targets; ceiling 304)\n`;

  return { stdout: out };
}
