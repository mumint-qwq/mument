#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 1 || $# -gt 2 || ( $# -eq 2 && ${2-} != --container ) ]]; then
  printf 'Usage: %s OUTPUT_DIRECTORY [--container]\n' "$0" >&2
  exit 1
fi
root=$(cd "$(dirname "$0")/../.." && pwd)
mkdir -p -- "$1"
out=$(cd "$1" && pwd)
scratch=$(mktemp -d)
embed=''
cleanup() {
  if [[ -n $embed ]]; then
    rm -- "$embed"
    rmdir -- "$(dirname "$embed")" 2>/dev/null || true
  fi
  rm -rf -- "$scratch"
}
trap cleanup EXIT
go_cmd=${GO:-go}
export TZ=Asia/Shanghai
export GOMAXPROCS=${GOMAXPROCS:-2}
export TMPDIR=${TMPDIR:-/tmp}

record() {
  local name=$1 code=0
  shift
  "$@" > "$out/$name.log" 2>&1 || code=$?
  printf '%s\n' "$code" > "$out/$name.exit"
  if [[ $code != 0 ]]; then
    cat "$out/$name.log" >&2
    return "$code"
  fi
}
revision=$(git -C "$root" rev-parse HEAD)
printf '%s\n' "$revision" > "$out/revision.txt"
record test-binary env CGO_ENABLED=0 "$go_cmd" -C "$root/backend" test -c -trimpath \
  -o "$out/e2-preview-tests" ./internal/preview
(cd "$out" && sha256sum e2-preview-tests) > "$out/e2-preview-tests.sha256"
"$go_cmd" version -m "$out/e2-preview-tests" > "$out/test-buildinfo.log"
record orphan "$out/e2-preview-tests" -test.run '^TestOrphan' -test.v -test.count=1

if [[ ${2-} == --container ]]; then
  mkdir "$scratch/container"
  cp -- "$out/e2-preview-tests" "$scratch/container/preview-tests"
  printf 'FROM scratch\nCOPY preview-tests /preview-tests\nENTRYPOINT ["/preview-tests"]\n' > "$scratch/container/Dockerfile"
  image="mument-e2-verify:${revision:0:12}"
  record container-build docker build -t "$image" "$scratch/container"
  docker image inspect --format '{{.Id}}' "$image" > "$out/container-image-id"
  record container docker run --rm --network none --read-only --cpus=2 \
    --memory=1g --memory-swap=2g --tmpfs /tmp:rw,nosuid,nodev \
    "$image" -test.run '^TestOrphan' -test.v -test.count=1
  exit
fi

record mount env MUMENT_ORPHAN_MOUNT_TEST=1 unshare --user --map-root-user --mount --propagation private \
  "$out/e2-preview-tests" -test.run '^TestOrphanPrivateMounts$' -test.v
record e1 env GO="$go_cmd" "$root/scripts/development/trial-007e.sh" "$out/e1"

embed_dir="$root/backend/internal/web/embed"
if ! compgen -G "$embed_dir/*" >/dev/null; then
  mkdir -p -- "$embed_dir"
  embed=$(mktemp "$embed_dir/e2-placeholder-XXXXXX")
  printf 'Synthetic handler test embed\n' > "$embed"
fi
record core "$go_cmd" -C "$root/backend" test -race -count=1 -v \
  ./internal/preview ./internal/previewwarmup ./internal/cacheledger \
  ./internal/adapters/fs/diskcache ./internal/database/sql ./internal/cachetrial
record entrypoints env MUMENT_MOVE_HANDLER_TRANSPORT=1 "$go_cmd" -C "$root/backend" \
  test -race ./internal/web -run '^(TestPreviewOrphan|TestLedgerMove|TestPreviewWarmupHTTP)' -count=1 -v
record move-1000 env MUMENT_MOVE_HANDLER_TRANSPORT=1 MUMENT_MOVE_IMAGE_COUNT=1000 "$go_cmd" -C "$root/backend" \
  test ./internal/web -run '^TestLedgerMoveHTTP$' -count=1 -v

d=2b3eb968f6a6a05fe653081cbd97f455cd88ffb1
git -C "$root" show -s --format='%H %T' "$d" > "$out/rollback-source.txt"
mkdir "$scratch/d" "$scratch/fixture"
git -C "$root" archive "$d" backend | tar -x -C "$scratch/d"
cp -- "$root/scripts/development/007e2-d-probe.go" \
  "$scratch/d/backend/internal/preview/007e2_rollback_probe_test.go"
record rollback-export env MUMENT_ORPHAN_ROLLBACK_FIXTURE="$scratch/fixture" \
  "$out/e2-preview-tests" -test.run '^TestOrphanExportRollback$' -test.v
record rollback-d env MUMENT_ORPHAN_ROLLBACK_FIXTURE="$scratch/fixture" "$go_cmd" \
  -C "$scratch/d/backend" test ./internal/preview -run '^TestDReadsE2Fixture$' -count=1 -v
