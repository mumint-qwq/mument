#!/usr/bin/env bash
# Run the same real-HTTP synthetic-photo harness against C and its A/B base.
set -euo pipefail
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$repo"
export GOMAXPROCS=2 TZ=Asia/Shanghai
baseline=a10edcbdac0b84c8f6c98f5e012709f2ad1d0dad
if [[ ! -s backend/internal/web/embed/index.html ]]; then
  echo 'Build the real embedded frontend with npm --prefix frontend run build first.' >&2
  exit 1
fi
mkdir -p .validation
out=$(mktemp -d "$repo/.validation/007c-scale.XXXXXX")
echo "Evidence directory: $out"
go version > "$out/go-version"
git rev-parse HEAD > "$out/candidate-head"
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
    for side in base candidate; do
      expected=0
      [[ "$side" == base ]] && expected=1
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
