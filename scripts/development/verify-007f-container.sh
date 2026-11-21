#!/usr/bin/env bash
# Docker operator only; synthetic fixtures, fixed test artifacts, unique containers.
set -euo pipefail
[[ $# == 1 ]] || { printf 'Usage: %s HOST_VERIFICATION_OUTPUT\n' "$0" >&2; exit 2; }
out=$(cd "$1" && pwd)
for binary in f-preview-tests f-reuse-tests f-ledger-tests e3-preview-tests; do [[ -x $out/$binary ]]; done
verify_code=0
(cd "$out" && sha256sum -c binaries.sha256) > "$out/container-artifacts-verify.log" 2>&1 || verify_code=$?
printf '%s\n' "$verify_code" > "$out/container-artifacts-verify.exit"
[[ $verify_code == 0 ]]
work=$(mktemp -d)
image=''
container=''
cleanup(){
 [[ -z $container ]] || docker rm -f "$container" >/dev/null 2>&1 || true
 [[ -z $image ]] || docker image rm "$image" >/dev/null 2>&1 || true
 rm -rf -- "$work"
}
trap cleanup EXIT
mkdir "$work/build" "$work/fixture"
for pair in f-preview:f-tests f-reuse:f-reuse-tests f-ledger:f-ledger-tests e3-preview:e3-tests; do
 cp "$out/${pair%%:*}-tests" "$work/build/${pair#*:}"
done
printf 'FROM scratch\nCOPY f-tests /f-tests\nCOPY f-reuse-tests /f-reuse-tests\nCOPY f-ledger-tests /f-ledger-tests\nCOPY e3-tests /e3-tests\n' > "$work/build/Dockerfile"
tag="mument-f-verify:$(date +%s)-$$"
build_code=0
docker build -t "$tag" "$work/build" > "$out/container-build.log" 2>&1 || build_code=$?
printf '%s\n' "$build_code" > "$out/container-build.exit"
[[ $build_code == 0 ]]
image=$tag
docker image inspect --format '{{.Id}}' "$image" > "$out/container-image-id"
run(){
 local name=$1 binary=$2 phase=$3 suite=$4 code=0
 local envs=(-e GOMAXPROCS=2 -e MUMENT_SPACE_CONTAINER_TEST=1)
 if [[ -n $phase ]]; then envs+=(-e MUMENT_REUSE_ROLLBACK=/fixture -e "MUMENT_REUSE_ROLLBACK_PHASE=$phase" -e MUMENT_REUSE_UNIQUE_MOUNTS=1); fi
 container=$(docker create --network none --read-only --user "$(id -u):$(id -g)" --cpus 2 --memory 1g --memory-swap 2g \
  --tmpfs /tmp:rw,nosuid,nodev --mount "type=bind,source=$work/fixture,target=/fixture" \
  "${envs[@]}" "$image" "$binary" -test.run "$suite" -test.v -test.count=1 -test.timeout=15m)
 printf '%s\n' "$container" > "$out/$name.container-id"
 docker start -a "$container" > "$out/$name.log" 2>&1 || code=$?
 docker inspect --format '{{.HostConfig.NanoCpus}} {{.HostConfig.Memory}} {{.HostConfig.MemorySwap}} {{.State.ExitCode}} {{.State.OOMKilled}}' "$container" > "$out/$name.state"
 printf '%s\n' "$code" > "$out/$name.exit"
 docker rm "$container" >/dev/null
 container=''
 local valid=0
 [[ $code == 0 && $(cat "$out/$name.state") == '2000000000 1073741824 2147483648 0 false' ]] || valid=1
 printf '%s\n' "$valid" > "$out/$name.validation.exit"
 [[ $valid == 0 ]]
}
# Core F execution is separate from version compatibility. The preview container
# also reads runtime cpu.max/memory.max/memory.swap.max/memory.events (oom=0).
run container-f-core /f-tests '' '^(TestContent.*|TestSpaceContainerLimits)$'
run container-f-worker /f-reuse-tests '' '^TestContent'
run container-f-ledger /f-ledger-tests '' '^TestContent'
# Every stage creates a new container, retaining only the explicit fixture bind.
run container-e3-start /e3-tests start '^TestFCompatibilityBaseline$'
run container-f-first /f-tests f-first '^(TestContentRollbackF|TestSpaceContainerLimits)$'
run container-e3-return /e3-tests old-return '^TestFCompatibilityBaseline$'
run container-f-recreated /f-tests f-return '^(TestContentRollbackF|TestSpaceContainerLimits)$'
cp "$work/fixture/state.json" "$out/container-rollback-state.json"
# STATX_MNT_ID_UNIQUE identifies mounts across namespaces; ordinary mnt_id may
# be recycled and is never accepted here as the cross-container evidence.
declare -A source_ids=() cache_ids=()
: > "$out/container-unique-mounts.txt"
for name in container-e3-start container-f-first container-e3-return container-f-recreated; do
 pair=$(sed -nE 's/.*source_unique_mount_id=([0-9]+) cache_unique_mount_id=([0-9]+).*/\1 \2/p' "$out/$name.log")
 read -r source_id cache_id <<< "$pair"
 if [[ -z $source_id || -z $cache_id || -n ${source_ids[$source_id]:-} || -n ${cache_ids[$cache_id]:-} ]]; then
  printf '1\n' > "$out/container-unique-mounts.exit"
  exit 1
 fi
 source_ids[$source_id]=1; cache_ids[$cache_id]=1
 printf '%s %s %s\n' "$name" "$source_id" "$cache_id" >> "$out/container-unique-mounts.txt"
done
printf '0\n' > "$out/container-unique-mounts.exit"
