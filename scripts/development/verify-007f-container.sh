#!/usr/bin/env bash
# Requires a Docker operator. Only synthetic fixtures and unique image/container IDs.
set -euo pipefail
[[ $# == 1 ]] || { printf 'Usage: %s HOST_VERIFICATION_OUTPUT\n' "$0" >&2; exit 2; }
out=$(cd "$1" && pwd)
[[ -x $out/f-preview-tests && -x $out/e3-preview-tests ]]
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
cp "$out/f-preview-tests" "$work/build/f-tests"
cp "$out/e3-preview-tests" "$work/build/e3-tests"
printf 'FROM scratch\nCOPY f-tests /f-tests\nCOPY e3-tests /e3-tests\n' > "$work/build/Dockerfile"
tag="mument-f-verify:$(date +%s)-$$"
docker build -t "$tag" "$work/build" > "$out/container-build.log" 2>&1
image=$tag
docker image inspect --format '{{.Id}}' "$image" > "$out/container-image-id"
run(){
 local name=$1 binary=$2 phase=$3 suite=$4 code=0
 container=$(docker create --network none --read-only --user "$(id -u):$(id -g)" --cpus 2 --memory 1g --memory-swap 2g \
  --tmpfs /tmp:rw,nosuid,nodev --mount "type=bind,source=$work/fixture,target=/fixture" \
  -e GOMAXPROCS=2 -e MUMENT_SPACE_CONTAINER_TEST=1 \
  -e MUMENT_REUSE_ROLLBACK=/fixture -e "MUMENT_REUSE_ROLLBACK_PHASE=$phase" \
  "$image" "$binary" -test.run "$suite" -test.v -test.count=1)
 printf '%s\n' "$container" > "$out/$name.container-id"
 docker start -a "$container" > "$out/$name.log" 2>&1 || code=$?
 docker inspect --format '{{.HostConfig.NanoCpus}} {{.HostConfig.Memory}} {{.HostConfig.MemorySwap}} {{.State.ExitCode}} {{.State.OOMKilled}}' "$container" > "$out/$name.state"
 printf '%s\n' "$code" > "$out/$name.exit"
 docker rm "$container" >/dev/null
 container=''
 [[ $code == 0 && $(cat "$out/$name.state") == '2000000000 1073741824 2147483648 0 false' ]]
}
# Every stage creates a new container, retaining only the explicit fixture bind.
run container-e3-start /e3-tests start '^TestFCompatibilityBaseline$'
run container-f-first /f-tests f-first '^(TestContentRollbackF|TestSpaceContainerLimits)$'
run container-e3-return /e3-tests old-return '^TestFCompatibilityBaseline$'
run container-f-recreated /f-tests f-return '^(TestContentRollbackF|TestSpaceContainerLimits)$'
cp "$work/fixture/state.json" "$out/container-rollback-state.json"
