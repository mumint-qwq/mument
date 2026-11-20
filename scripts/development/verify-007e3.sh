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
image=''
container=''
embed=''
cleanup() {
  if [[ -n $embed ]]; then
    rm -- "$embed"
    rmdir -- "$(dirname "$embed")" 2>/dev/null || true
  fi
  if [[ -n $container ]]; then
    docker rm -f "$container" >/dev/null 2>&1 || true
  fi
  if [[ -n $image ]]; then
    docker image rm "$image" >/dev/null 2>&1 || true
  fi
  rm -rf -- "$scratch"
}
trap cleanup EXIT
go_cmd=${GO:-go}
export GO="$go_cmd" TZ=Asia/Shanghai GOMAXPROCS=${GOMAXPROCS:-2}
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
  -o "$out/e3-preview-tests" ./internal/preview
(cd "$out" && sha256sum e3-preview-tests) > "$out/e3-preview-tests.sha256"
"$go_cmd" version -m "$out/e3-preview-tests" > "$out/test-buildinfo.log"
record space-and-capacity "$out/e3-preview-tests" -test.run '^(TestSpace|TestCapacity)' -test.v -test.count=1

if [[ ${2-} == --container ]]; then
  mkdir "$scratch/container" "$scratch/fixture"
  cp -- "$out/e3-preview-tests" "$scratch/container/preview-tests"
  printf 'FROM scratch\nCOPY preview-tests /preview-tests\nENTRYPOINT ["/preview-tests"]\n' > "$scratch/container/Dockerfile"
  tag="mument-e3-verify:${revision:0:12}-${scratch##*.}"
  record container-build docker build -t "$tag" "$scratch/container"
  image=$tag
  docker image inspect --format '{{.Id}}' "$image" > "$out/container-image-id"
  replay_container() {
    local name=$1 suite=$2 ret=0
    shift 2
    container=$(docker create --network none --read-only --cpus=2 --memory=1g --memory-swap=2g \
      --tmpfs /tmp:rw,nosuid,nodev -e GOMAXPROCS=2 -e MUMENT_SPACE_CONTAINER_TEST=1 \
      "$@" "$image" -test.run "$suite" -test.v -test.count=1)
    printf '%s\n' "$container" > "$out/$name.container-id"
    record "$name" docker start -a "$container" || ret=$?
    docker inspect --format '{{.State.ExitCode}} {{.State.OOMKilled}}' "$container" > "$out/$name.state"
    docker rm "$container" >/dev/null
    container=''
    [[ $ret == 0 && $(cat "$out/$name.state") == '0 false' ]]
  }
  replay_container container-core '^(TestSpace|TestCapacity|TestOrphan)'
  bind=(--mount "type=bind,source=$scratch/fixture,target=/fixture" -e MUMENT_CAPACITY_ROLLBACK=/fixture)
  replay_container container-isolate '^(TestCapacityExportRollback|TestSpaceContainerLimits)$' "${bind[@]}"
  replay_container container-recreate '^(TestCapacityContainerResume|TestSpaceContainerLimits)$' "${bind[@]}"
  exit
fi

record fs-binary env CGO_ENABLED=0 "$go_cmd" -C "$root/backend" test -c -trimpath \
  -o "$scratch/e3-filesystem-tests" ./internal/adapters/fs/diskcache
record private-space-mount env MUMENT_SPACE_MOUNT_TEST=1 unshare --user --map-root-user --mount --propagation private \
  "$scratch/e3-filesystem-tests" -test.run '^TestSpaceSmallPrivateFilesystem$' -test.v
mkdir "$scratch/private-rebind"
record private-rebind-export env MUMENT_SPACE_CONTAINER_TEST=1 MUMENT_CAPACITY_ROLLBACK="$scratch/private-rebind" \
  "$out/e3-preview-tests" -test.run '^TestCapacityExportRollback$' -test.v
record private-rebind env MUMENT_SPACE_CONTAINER_TEST=1 MUMENT_CAPACITY_ROLLBACK="$scratch/private-rebind" \
  unshare --user --map-root-user --mount --propagation private \
  "$out/e3-preview-tests" -test.run '^TestCapacityContainerResume$' -test.v
record e3-race "$go_cmd" -C "$root/backend" test -race -count=1 -v -run '^(TestSpace|TestCapacity)' \
  ./internal/preview ./internal/adapters/fs/diskcache ./internal/cacheledger ./internal/previewwarmup
record e2-regression "$root/scripts/development/verify-007e2.sh" "$out/e2"
record settings "$go_cmd" -C "$root/backend" test -race -count=1 -run '^TestPreviewSpaceValidation$' ./pkg/settings

embed_dir="$root/backend/internal/web/embed"
if ! compgen -G "$embed_dir/*" >/dev/null; then
  mkdir -p -- "$embed_dir"
  embed=$(mktemp "$embed_dir/e3-placeholder-XXXXXX")
  printf 'Synthetic E3 handler test embed\n' > "$embed"
fi
record admin-http env MUMENT_MOVE_HANDLER_TRANSPORT=1 "$go_cmd" -C "$root/backend" \
  test -race -count=1 -run '^TestPreviewSpaceHTTPHandlers$' -v ./internal/web

for older in e2 d; do
  if [[ $older == e2 ]]; then
    frozen=e425444bd7979467376e18b0a88c752ec0538e2c
  else
    frozen=2b3eb968f6a6a05fe653081cbd97f455cd88ffb1
  fi
  git -C "$root" show -s --format='%H %T' "$frozen" > "$out/rollback-$older-source.txt"
  mkdir "$scratch/$older" "$scratch/fixture-$older"
  git -C "$root" archive "$frozen" backend | tar -x -C "$scratch/$older"
  cp -- "$root/scripts/development/007e3-rollback-probe.go" \
    "$scratch/$older/backend/internal/preview/007e3_rollback_probe_test.go"
  export MUMENT_CAPACITY_ROLLBACK="$scratch/fixture-$older"
  record "export-$older" "$out/e3-preview-tests" -test.run '^TestCapacityExportRollback$' -test.v
  record "rollback-$older" "$go_cmd" -C "$scratch/$older/backend" test ./internal/preview -run '^TestOldReadsE3CapacityFixture$' -count=1 -v
  record "return-$older" "$out/e3-preview-tests" -test.run '^TestCapacityReturnAfterRollback$' -test.v
done
