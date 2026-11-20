#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  printf 'Usage: %s OUTPUT_DIRECTORY\n' "$0" >&2
  exit 1
fi

root=$(cd "$(dirname "$0")/../.." && pwd)
mkdir -p -- "$1"
out=$(cd "$1" && pwd)
fixture=$(mktemp -d)
trap 'rm -rf -- "$fixture"' EXIT
mkdir "$fixture/inputs"
go_cmd=${GO:-go}

record() {
  local name=$1 expected=$2 code=0
  shift 2
  "$@" > "$out/$name.log" 2>&1 || code=$?
  printf '%s\n' "$code" > "$out/$name.exit"
  if [[ $code != "$expected" ]]; then
    cat "$out/$name.log" >&2
    return 1
  fi
}

record tests 0 "$go_cmd" -C "$root/backend" test ./internal/cachetrial \
  -count=1 -v -args -trial-fixture-output "$fixture/inputs"
record cli-build 0 "$go_cmd" -C "$root/backend" build -trimpath \
  -o "$fixture/preview-cache-trial" ./cmd/preview-cache-trial
(cd "$fixture" && sha256sum preview-cache-trial) > "$out/cli.sha256"
"$go_cmd" version -m "$fixture/preview-cache-trial" > "$out/cli-buildinfo.log"

trial() {
  local name=$1 expected=$2 code=0
  shift 2
  "$fixture/preview-cache-trial" -snapshot "$fixture/inputs/snapshot.sqlite" \
    -cache-dir "$fixture/inputs/cache" -samples 50 "$@" \
    > "$out/$name.json" 2> "$out/$name.stderr" || code=$?
  printf '%s\n' "$code" > "$out/$name.exit"
  if [[ $code != "$expected" ]]; then
    cat "$out/$name.stderr" >&2
    return 1
  fi
}

# The validly named symlink leaves an explicit inventory coverage gap.
trial synthetic 2 -fixture-evidence "$fixture/inputs/evidence.json"
trial ledger-only 2
trial first-page 2 -max-entries 4 -max-bytes 128
readarray -t cursor < <(python3 - "$out/first-page.json" <<'PY'
import json
import sys
from pathlib import Path

report = json.loads(Path(sys.argv[1]).read_text())
print(report['snapshot_id'])
print(report['ledger']['next_result'])
print(report['inventory']['next_cache'])
PY
)
trial next-page 2 -max-entries 4 -max-bytes 128 -expect-snapshot "${cursor[0]}" \
  -after-result "${cursor[1]}" -after-cache "${cursor[2]}"
trial completed-window 0 -expect-snapshot "${cursor[0]}" -after-cache done
python3 - "$out" <<'PY'
import json
import sys
from pathlib import Path

out = Path(sys.argv[1])
synthetic, ledger, first, second = (
    json.loads((out / f'{name}.json').read_text())
    for name in ('synthetic', 'ledger-only', 'first-page', 'next-page')
)
assert synthetic['estimated_candidates'] == 2 and synthetic['executable_deletes'] == 0
assert ledger['estimated_candidates'] == 0 and ledger['executable_deletes'] == 0
assert second['ledger']['after_result'] == first['ledger']['next_result']
assert second['ledger']['next_result'] > first['ledger']['next_result']
assert first['inventory']['next_cache'] != second['inventory']['next_cache']
PY
