#!/usr/bin/env bash
# pprof_dump.sh — collect goroutine / heap / allocs dumps from a running
# lampac-go server. Writes to ./pprof-dumps/ (relative to repo root).
#
# Usage:
#   ./scripts/pprof_dump.sh                     # localhost defaults
#   ./scripts/pprof_dump.sh http://1.2.3.4:9118 # remote host
#   METRICS_TOKEN=secret ./scripts/pprof_dump.sh http://1.2.3.4:9118
#
# After Sprint 1, /debug/pprof/* is gated by metricsAccessGuard:
#   - localhost (127.0.0.1, ::1) works without auth
#   - remote requires Bearer/Basic token from [observability] metrics_auth
#
# Each dump goes to a separate file under pprof-dumps/<timestamp>/.

set -euo pipefail

BASE="${1:-http://127.0.0.1:888}"
TOKEN="${METRICS_TOKEN:-}"
STAMP="$(date +%Y%m%d-%H%M%S)"
OUT="pprof-dumps/${STAMP}"
mkdir -p "$OUT"

AUTH_ARGS=()
if [[ -n "$TOKEN" ]]; then
  AUTH_ARGS=(-H "Authorization: Bearer ${TOKEN}")
fi

echo "→ Dumping pprof from ${BASE} to ${OUT}/"

# 1) Goroutines (debug=2 = full stacks, human-readable, BIG)
curl -sS --max-time 30 "${AUTH_ARGS[@]}" \
  "${BASE}/debug/pprof/goroutine?debug=2" \
  -o "${OUT}/goroutine-full.txt"
echo "  ✓ goroutine-full.txt  ($(wc -l < "${OUT}/goroutine-full.txt") lines)"

# 2) Goroutines (debug=1 = aggregated by stack, small + readable)
curl -sS --max-time 30 "${AUTH_ARGS[@]}" \
  "${BASE}/debug/pprof/goroutine?debug=1" \
  -o "${OUT}/goroutine-summary.txt"
echo "  ✓ goroutine-summary.txt  ($(wc -l < "${OUT}/goroutine-summary.txt") lines)"

# 3) Heap (pprof binary format, use `go tool pprof` to inspect)
curl -sS --max-time 30 "${AUTH_ARGS[@]}" \
  "${BASE}/debug/pprof/heap" \
  -o "${OUT}/heap.pprof"
echo "  ✓ heap.pprof  ($(wc -c < "${OUT}/heap.pprof") bytes)"

# 4) Allocs (since program start; pprof binary)
curl -sS --max-time 30 "${AUTH_ARGS[@]}" \
  "${BASE}/debug/pprof/allocs" \
  -o "${OUT}/allocs.pprof"
echo "  ✓ allocs.pprof  ($(wc -c < "${OUT}/allocs.pprof") bytes)"

# 5) Goroutine count summary — fast triage
TOP=$(grep -E '^[0-9]+ @' "${OUT}/goroutine-summary.txt" 2>/dev/null \
        | sort -rn | head -20)
echo ""
echo "─── Top 20 goroutine groups (count @ creator) ───"
echo "$TOP"

echo ""
echo "─── Inspect with: ───"
echo "  go tool pprof -http=:8080 ${OUT}/heap.pprof"
echo "  go tool pprof -http=:8080 ${OUT}/allocs.pprof"
echo "  less ${OUT}/goroutine-summary.txt"
echo "  less ${OUT}/goroutine-full.txt   # full stacks — search for repeating callers"
