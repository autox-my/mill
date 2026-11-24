#!/usr/bin/env bash
# The one gate for mill: "green" means this script exits 0.
# Every step prints PASS, FAIL or SKIP. A SKIP (a missing tool) fails the gate.
set -uo pipefail
cd "$(dirname "$0")/.."

GOTESTSUM=gotest.tools/gotestsum@v1.12.0

status=0
log=build/check
mkdir -p "$log"

step() {
  local name=$1; shift
  local out="$log/${name// /-}.txt"
  if "$@" >"$out" 2>&1; then echo "PASS $name"; else echo "FAIL $name"; sed 's/^/     /' "$out" | tail -40; status=1; fi
}

need() {
  command -v "$1" >/dev/null 2>&1 && return 0
  echo "SKIP $1 not found (a SKIP fails the gate)"; status=1; return 1
}

if need go; then
  gofiles=$(git ls-files --cached --others --exclude-standard '*.go')
  if [ -n "$gofiles" ]; then
    step gofmt sh -c 'test -z "$(gofmt -l "$@")"' _ $gofiles
    step "go vet" go vet ./...
  fi
  rm -f build/junit.xml
  step tests go run "$GOTESTSUM" --junitfile build/junit.xml -- -race -cover -count=1 ./...
fi

if need req; then
  step "req canon" req canon --fail-on-flag
  # Once a human signs the first Baseline under baselines/, switch this to `req check` + `req baseline --verify`.
  step "req gate" req gate --test-report build/junit.xml
fi

[ "$status" -eq 0 ] && echo "verdict: GREEN" || echo "verdict: RED"
exit "$status"
