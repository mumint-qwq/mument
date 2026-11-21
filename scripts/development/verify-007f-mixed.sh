#!/usr/bin/env bash
set -euo pipefail
[[ $# == 1 ]] || { printf 'Usage: %s OUTPUT_DIRECTORY\n' "$0" >&2; exit 2; }
root=$(cd "$(dirname "$0")/../.." && pwd)
mkdir -p -- "$1";out=$(cd "$1" && pwd)
go_cmd=${GO:-go};export GOMAXPROCS=2 TZ=Asia/Shanghai
CGO_ENABLED=0 "$go_cmd" -C "$root/backend" test -c -trimpath -o "$out/mixed-tests" ./internal/previewwarmup > "$out/build.log" 2>&1
(cd "$out" && sha256sum mixed-tests) > "$out/binaries.sha256"
git -C "$root" show -s --format='%H %T' HEAD > "$out/source.txt"
date -Is > "$out/started.txt"
code=0
MUMENT_REUSE_MIXED_DURATION=${MUMENT_REUSE_MIXED_DURATION:-30m} \
 unshare --user --map-root-user --mount --propagation private "$out/mixed-tests" \
 -test.run '^TestContentMixedBackground$' -test.v -test.count=1 -test.timeout=45m > "$out/mixed.log" 2>&1 || code=$?
printf '%s\n' "$code" > "$out/mixed.exit"
date -Is > "$out/finished.txt"
exit "$code"
