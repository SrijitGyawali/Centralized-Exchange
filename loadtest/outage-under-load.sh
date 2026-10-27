#!/usr/bin/env bash
# The week-7 failure drill: trading under steady load while PostgreSQL is
# frozen for 30 seconds and slow WebSocket viewers keep getting dropped.
#
#   make up                            # the Compose stack must be running
#   loadtest/outage-under-load.sh evidence/load/<date>-outage
#
# What must be true afterwards (read the report and timeline.csv):
#   - trading latency in stage 2 (the outage) looks like stages 1 and 3
#   - projection lag grows during the freeze, then returns to ~0
#   - projection failures are counted; zero commands are lost or doubled
#   - slow viewers are dropped and reconnect; no trading error comes from them
#
# Timeline (workloads/outage.json): 10 s warmup, then stage 1 (20 s, normal),
# stage 2 (40 s, PostgreSQL paused from its 5th to its 35th second),
# stage 3 (60 s, recovery).
set -euo pipefail
out=${1:?usage: outage-under-load.sh <output-dir>}
cd "$(dirname "$0")/.."

# `docker pause` freezes every process in the container (cgroup freezer):
# connections stay open but nothing answers, the nastiest kind of outage,
# because clients hang instead of failing fast.
pg=$(docker compose -f deploy/docker-compose.yml ps -q postgres)
[ -n "$pg" ] || { echo "PostgreSQL is not running: make up first" >&2; exit 1; }
trap 'docker unpause "$pg" >/dev/null 2>&1 || true' EXIT # never leave it frozen

exe=tmp/loadgen$(go env GOEXE) # built first, so compiling cannot shift the timeline
go build -o "$exe" ./cmd/loadgen
"$exe" run -workload loadtest/workloads/outage.json -out "$out" \
  -label "postgres paused 30s + 5 slow viewers" -slow-viewers 5 -slow-read 5s \
  -note "${NOTE:-describe the machine}" &
lg=$!

sleep 35 # warmup 10 s + stage 1 20 s + 5 s into stage 2
echo "outage: pausing PostgreSQL at $(date +%T)"
docker pause "$pg" >/dev/null
sleep 30
echo "outage: unpausing PostgreSQL at $(date +%T)"
docker unpause "$pg" >/dev/null

wait "$lg"
echo "results in $out (see report.md and timeline.csv)"
