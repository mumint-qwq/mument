#!/usr/bin/env bash
# Run the same loopback-HTTP harness against the current C and A/B or RC1.
set -euo pipefail
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$repo"
export GOMAXPROCS=2 TZ=Asia/Shanghai
baseline=a10edcbdac0b84c8f6c98f5e012709f2ad1d0dad
baseline_decodes=1
if [[ $# == 1 && $1 == --compare-rc1 ]]; then
  baseline=fe73d9f04afd06f86d65ef1455f0644572151158
  baseline_decodes=0
elif [[ $# != 0 ]]; then
  echo "Usage: $0 [--compare-rc1]" >&2
  exit 2
fi
if [[ ! -s backend/internal/web/embed/index.html ]]; then
  echo 'Build the real embedded frontend with npm --prefix frontend run build first.' >&2
  exit 1
fi
mkdir -p .validation
out=$(mktemp -d "$repo/.validation/007c-scale.XXXXXX")
echo "Evidence directory: $out"
go version > "$out/go-version"
git rev-parse HEAD > "$out/candidate-head"
git rev-parse "$baseline" > "$out/base-head"
git status --short > "$out/candidate-status"
mkdir "$out/base-source"
git archive "$baseline" | tar -x -C "$out/base-source"
cp -a backend/internal/web/embed "$out/base-source/backend/internal/web/embed"
cp backend/internal/web/cache_move_test.go "$out/base-source/backend/internal/web/cache_move_test.go"
go -C "$out/base-source/backend" test -c -o "$out/base.test" ./internal/web
go -C backend test -c -o "$out/candidate.test" ./internal/web
sha256sum "$out/base.test" "$out/candidate.test" > "$out/binaries.sha256"

failed=0
for count in 100 1000; do
  for round in 1 2 3; do
    order=(base candidate)
    if [[ "$round" == 2 ]]; then order=(candidate base); fi
    for side in "${order[@]}"; do
      expected=0
      if [[ "$side" == base ]]; then expected=$baseline_decodes; fi
      name="$out/$side-$count-$round"
      result=0
      MUMENT_MOVE_IMAGE_COUNT="$count" MUMENT_MOVE_EXPECT_BASELINE="$expected" \
        "$out/$side.test" -test.v -test.run '^TestLedgerMoveHTTP$' > "$name.log" 2>&1 || result=$?
      printf '%s\n' "$result" > "$name.exit"
      printf '%s %s images, round %s, exit %s\n' "$side" "$count" "$round" "$result"
      if [[ "$result" != 0 ]]; then failed=1; fi
    done
  done
done
exit "$failed"
