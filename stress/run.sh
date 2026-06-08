#!/usr/bin/env bash
# Run the Pi-Bench stress matrix against a running stack.
#
# Two modes:
#   container (default) — runs k6 in a container on the stack's docker network,
#                         hitting http://lb:80. Works everywhere, including macOS
#                         and `internal: true` networks where the published host
#                         port is not reachable.
#   host                — K6_MODE=host runs a locally-installed k6 against
#                         BASE_URL (default http://localhost:8080).
#
# Usage:
#   stress/run.sh [steady|spike|capacity|maxrps|endurance]
#   K6_MODE=host stress/run.sh capacity
#   NET=mystack_backend stress/run.sh maxrps
set -euo pipefail

SCENARIO="${1:-steady}"
case "$SCENARIO" in
  steady|spike|capacity|maxrps|endurance) ;;
  *) echo "usage: run.sh [steady|spike|capacity|maxrps|endurance]" >&2; exit 2 ;;
esac

DIR="$(cd "$(dirname "$0")" && pwd)"
K6_MODE="${K6_MODE:-container}"
DEVICE_COUNT="${DEVICE_COUNT:-50}"

echo ">> scenario=$SCENARIO mode=$K6_MODE devices=$DEVICE_COUNT"

if [ "$K6_MODE" = "host" ]; then
  command -v k6 >/dev/null || { echo "k6 not installed; use container mode" >&2; exit 1; }
  BASE_URL="${BASE_URL:-http://localhost:8080}"
  echo ">> BASE_URL=$BASE_URL"
  exec k6 run --env BASE_URL="$BASE_URL" --env DEVICE_COUNT="$DEVICE_COUNT" \
    --env SCENARIO="$SCENARIO" "$DIR/stress.js"
fi

# container mode
NET="${NET:-$(docker network ls --format '{{.Name}}' | grep -m1 backend || true)}"
[ -n "$NET" ] || { echo "No *backend network found. Start the stack first: docker compose up -d" >&2; exit 1; }
BASE_URL="${BASE_URL:-http://lb:80}"
echo ">> network=$NET BASE_URL=$BASE_URL"
docker pull -q grafana/k6:latest >/dev/null
exec docker run --rm --network "$NET" \
  -e BASE_URL="$BASE_URL" -e DEVICE_COUNT="$DEVICE_COUNT" -e SCENARIO="$SCENARIO" \
  -v "$DIR":/stress grafana/k6:latest run /stress/stress.js
