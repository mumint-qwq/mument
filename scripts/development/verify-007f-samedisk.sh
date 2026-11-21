#!/usr/bin/env bash
# Same-disk C regression: E3 and F, three rounds, identical unique JPEG fixtures.
set -euo pipefail
[[ $# == 1 ]] || { printf 'Usage: %s OUTPUT_DIRECTORY\n' "$0" >&2; exit 2; }
root=$(cd "$(dirname "$0")/../.." && pwd)
mkdir -p -- "$1";out=$(cd "$1" && pwd)
go_cmd=${GO:-go};export GOMAXPROCS=2 TZ=Asia/Shanghai MUMENT_MOVE_HANDLER_TRANSPORT=1
scratch=$(mktemp -d)
placeholder=''
cleanup(){
 if [[ -n $placeholder ]]; then rm -f -- "$placeholder"; rmdir "$root/backend/internal/web/embed" 2>/dev/null || true; fi
 rm -rf -- "$scratch"
}
trap cleanup EXIT
if ! compgen -G "$root/backend/internal/web/embed/*" >/dev/null; then
 mkdir -p "$root/backend/internal/web/embed"
 placeholder=$(mktemp "$root/backend/internal/web/embed/007f-scale-placeholder-XXXXXX")
 printf 'Synthetic same-disk test embed only\n' > "$placeholder"
fi
mkdir "$scratch/e3"
git -C "$root" archive aaaefebaf29084dc22fc5f0b03cbaeb73aafdb59 backend | tar -x -C "$scratch/e3"
mkdir -p "$scratch/e3/backend/internal/web/embed"
printf 'Synthetic test embed\n' > "$scratch/e3/backend/internal/web/embed/test-placeholder"
CGO_ENABLED=0 "$go_cmd" -C "$scratch/e3/backend" test -c -trimpath -o "$out/e3-web-tests" ./internal/web
CGO_ENABLED=0 "$go_cmd" -C "$root/backend" test -c -trimpath -o "$out/f-web-tests" ./internal/web
(cd "$out" && sha256sum e3-web-tests f-web-tests) > "$out/binaries.sha256"
git -C "$root" show -s --format='%H %T' HEAD > "$out/source.txt"
failed=0
for round in 1 2 3;do
 order=(e3 f);if [[ $round == 2 ]];then order=(f e3);fi
 for side in "${order[@]}";do
  code=0
  MUMENT_MOVE_IMAGE_COUNT=1000 MUMENT_MOVE_EXPECT_BASELINE=0 "$out/$side-web-tests" -test.run '^TestLedgerMoveHTTP$' -test.v -test.count=1 > "$out/$side-$round.log" 2>&1 || code=$?
  printf '%s\n' "$code" > "$out/$side-$round.exit"
  [[ $code == 0 ]] || failed=1
 done
done
exit "$failed"
