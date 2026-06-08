#!/usr/bin/env bash
# Convenience wrapper for the score estimate.
#
# All scoring math lives in stress.js (handleSummary) — this script only does the
# one thing k6 cannot: sample aggregate container RSS/CPU (docker stats) so the
# efficiency dimension can be scored. Two passes:
#   1. run steady@RATE while sampling RSS/CPU  (output discarded)
#   2. re-run briefly, passing the measured RSS_MB/CPU_PCT, so stress.js prints
#      the full score (efficiency + tail_latency + gate, plus any env dims).
#
# Usage: stress/score.sh
#        RATE=200 DURATION=30s stress/score.sh
#        MAX_RPS=1800 SPIKE_P99=9 SPIKE_ERR=0 LAT_DRIFT=1.02 RSS_DRIFT=1.01 stress/score.sh
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
NET="${NET:-$(docker network ls --format '{{.Name}}' | grep -m1 backend || true)}"
[ -n "$NET" ] || { echo "No *backend network; start the stack first." >&2; exit 1; }
BASE_URL="${BASE_URL:-http://lb:80}"
RATE="${RATE:-200}"; DURATION="${DURATION:-30s}"
STATS="$(mktemp)"
CTRS=$(docker network inspect "$NET" -f '{{range .Containers}}{{.Name}} {{end}}')

k6run() { # k6run <duration> [extra -e args...]
  local dur="$1"; shift
  docker run --rm --network "$NET" \
    -e BASE_URL="$BASE_URL" -e DEVICE_COUNT="${DEVICE_COUNT:-50}" \
    -e SCENARIO=rate -e RATE="$RATE" -e DURATION="$dur" "$@" \
    -v "$DIR":/stress grafana/k6:latest run /stress/stress.js
}

docker pull -q grafana/k6:latest >/dev/null
echo ">> pass 1: load steady@${RATE} for ${DURATION}, sampling RSS/CPU of: $CTRS"

# Background RSS/CPU sampler (BSD-awk safe).
(
  for _ in $(seq 1 60); do
    docker stats --no-stream --format '{{.MemUsage}} {{.CPUPerc}}' $CTRS 2>/dev/null \
      | awk '
        function mb(s){ if(s~/GiB/){sub(/GiB.*/,"",s);return s*1024}
                        if(s~/MiB/){sub(/MiB.*/,"",s);return s*1.048576}
                        if(s~/KiB/){sub(/KiB.*/,"",s);return s/1024} return 0 }
        { used+=mb($1); c=$NF; gsub(/%/,"",c); cpu+=c }
        END{ printf "%.1f %.1f\n", used, cpu }'
  done
) > "$STATS" &
SAMPLER=$!
k6run "$DURATION" >/dev/null 2>&1 || true
kill "$SAMPLER" 2>/dev/null || true; wait "$SAMPLER" 2>/dev/null || true

NS=$(grep -c . "$STATS" || echo 0)
if [ "$NS" -gt 0 ]; then
  IDX=$(awk -v n="$NS" 'BEGIN{i=int(0.95*n); if(i<1)i=1; print i}')
  RSS_MB=$(awk '{print $1}' "$STATS" | sort -n | sed -n "${IDX}p" | sed 's/\..*//')
  CPU_PCT=$(awk '{c+=$2;n++} END{if(n)printf "%.1f",c/n; else print 0}' "$STATS")
else
  RSS_MB=""; CPU_PCT=""
fi
rm -f "$STATS"
echo ">> measured efficiency inputs: rss_p95=${RSS_MB:-?}MB cpu_avg=${CPU_PCT:-?}% (over ${NS} samples)"
echo ">> pass 2: scoring run"

k6run 15s \
  -e RSS_MB="${RSS_MB}" -e CPU_PCT="${CPU_PCT}" \
  -e MAX_RPS="${MAX_RPS:-}" -e SPIKE_P99="${SPIKE_P99:-}" -e SPIKE_ERR="${SPIKE_ERR:-}" \
  -e LAT_DRIFT="${LAT_DRIFT:-}" -e RSS_DRIFT="${RSS_DRIFT:-}"
