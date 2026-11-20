#!/usr/bin/env bash
# Isolated synthetic validation; never touches production or pushes Git.
set -euo pipefail
[[ $# == 1 ]] || { printf 'Usage: %s OUTPUT_DIRECTORY\n' "$0" >&2; exit 2; }
root=$(cd "$(dirname "$0")/../.." && pwd)
mkdir -p -- "$1"
out=$(cd "$1" && pwd)
export TZ=Asia/Shanghai GOMAXPROCS=${GOMAXPROCS:-2}
go_cmd=${GO:-go}
scratch=$(mktemp -d)
placeholder=''
cleanup(){
 if [[ -n $placeholder ]]; then rm -f -- "$placeholder"; rmdir "$root/backend/internal/web/embed" 2>/dev/null || true; fi
 rm -rf -- "$scratch"
}
trap cleanup EXIT
failures=0
record(){
 local name=$1 code=0
 shift
 "$@" > "$out/$name.log" 2>&1 || code=$?
 printf '%s\n' "$code" > "$out/$name.exit"
 if [[ $code != 0 ]]; then printf '%s failed (%s): %s\n' "$name" "$code" "$out/$name.log" >&2; failures=$((failures+1)); fi
}
git -C "$root" show -s --format='%H %T' HEAD > "$out/source.txt"
git -C "$root" diff --binary HEAD > "$out/worktree.patch"
date -Is > "$out/started.txt"
"$go_cmd" version > "$out/go-version.txt"
if ! compgen -G "$root/backend/internal/web/embed/*" >/dev/null; then
 mkdir -p "$root/backend/internal/web/embed"
 placeholder=$(mktemp "$root/backend/internal/web/embed/007f-placeholder-XXXXXX")
 printf 'Synthetic test embed only\n' > "$placeholder"
fi
record targeted "$go_cmd" -C "$root/backend" test -p 1 -count=1 -v -run '^(TestContent|TestPreviewReuse)' ./internal/cacheledger ./internal/preview ./internal/previewwarmup ./internal/adapters/fs/files ./internal/web ./pkg/settings
record race "$go_cmd" -C "$root/backend" test -race -p 1 -count=1 ./internal/cacheledger ./internal/preview ./internal/previewwarmup
record all env MUMENT_MOVE_HANDLER_TRANSPORT=1 "$go_cmd" -C "$root/backend" test -p 1 -count=1 ./...
record files-build env CGO_ENABLED=0 "$go_cmd" -C "$root/backend" test -c -trimpath -o "$out/f-files-tests" ./internal/adapters/fs/files
if [[ -x $out/f-files-tests ]]; then
 record private-exdev env MUMENT_REUSE_MOUNT_TEST=1 unshare --user --map-root-user --mount --propagation private "$out/f-files-tests" -test.run '^TestContentRealEXDEVCopyHint$' -test.v
fi
record preview-build env CGO_ENABLED=0 "$go_cmd" -C "$root/backend" test -c -trimpath -o "$out/f-preview-tests" ./internal/preview
# E3 -> F -> E3 -> F uses the same real cache, index identities and ledger.
mkdir "$scratch/e3" "$scratch/rollback"
git -C "$root" archive aaaefebaf29084dc22fc5f0b03cbaeb73aafdb59 backend | tar -x -C "$scratch/e3"
cp "$root/scripts/development/007f-rollback-probe.go" "$scratch/e3/backend/internal/preview/content_rollback_probe_test.go"
record baseline-build env CGO_ENABLED=0 "$go_cmd" -C "$scratch/e3/backend" test -c -trimpath -o "$out/e3-preview-tests" ./internal/preview
if [[ -x $out/e3-preview-tests && -x $out/f-preview-tests ]]; then
 export MUMENT_REUSE_ROLLBACK="$scratch/rollback"
 record rollback-e3-start env MUMENT_REUSE_ROLLBACK_PHASE=start "$out/e3-preview-tests" -test.run '^TestFCompatibilityBaseline$' -test.v
 record rollback-f-first env MUMENT_REUSE_ROLLBACK_PHASE=f-first "$out/f-preview-tests" -test.run '^TestContentRollbackF$' -test.v
 record rollback-e3-return env MUMENT_REUSE_ROLLBACK_PHASE=old-return "$out/e3-preview-tests" -test.run '^TestFCompatibilityBaseline$' -test.v
 record rollback-f-return env MUMENT_REUSE_ROLLBACK_PHASE=f-return "$out/f-preview-tests" -test.run '^TestContentRollbackF$' -test.v
 cp "$scratch/rollback/state.json" "$out/rollback-state.json"
fi
(cd "$out" && sha256sum f-files-tests f-preview-tests e3-preview-tests) > "$out/binaries.sha256"
"$go_cmd" version -m "$out/f-preview-tests" > "$out/buildinfo.txt"
date -Is > "$out/finished.txt"
printf '%s\n' "$failures" > "$out/failed-stages.txt"
[[ $failures == 0 ]]
