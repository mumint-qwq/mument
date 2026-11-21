#!/usr/bin/env bash
set -euo pipefail
[[ $# == 1 ]] || { printf 'Usage: %s OUTPUT_DIRECTORY\n' "$0" >&2; exit 2; }
root=$(cd "$(dirname "$0")/../.." && pwd)
mkdir -p -- "$1"; out=$(cd "$1" && pwd)
go_cmd=${GO:-go}
export GOMAXPROCS=2 TZ=Asia/Shanghai
scratch=$(mktemp -d);trap 'rm -rf -- "$scratch"' EXIT
mkdir "$scratch/e3"
git -C "$root" archive aaaefebaf29084dc22fc5f0b03cbaeb73aafdb59 backend | tar -x -C "$scratch/e3"
cp "$root/backend/internal/adapters/fs/files/cache_reuse_scale_test.go" "$scratch/e3/backend/internal/adapters/fs/files/"
cat > "$scratch/e3/backend/internal/adapters/fs/files/cache_reuse_scale_f_test.go" <<'GO'
package files
import("testing";"github.com/gtsteffaniak/filebrowser/backend/internal/preview";"github.com/gtsteffaniak/filebrowser/backend/pkg/indexing/iteminfo")
const contentScaleIsF=false
func contentScaleConfigure(a,b string){}
func contentScaleProve(t *testing.T,s *preview.Service,f iteminfo.ExtendedFileInfo)int64{return 0}
func contentScaleRetention(t *testing.T,s *preview.Service)(int,int){return 0,0}
GO
CGO_ENABLED=0 "$go_cmd" -C "$scratch/e3/backend" test -c -trimpath -o "$out/e3-files-tests" ./internal/adapters/fs/files
CGO_ENABLED=0 "$go_cmd" -C "$root/backend" test -c -trimpath -o "$out/f-files-tests" ./internal/adapters/fs/files
(cd "$out" && sha256sum e3-files-tests f-files-tests) > "$out/binaries.sha256"
git -C "$root" show -s --format='%H %T' HEAD > "$out/source.txt"
failed=0
for round in 1 2 3; do
 order=(e3 f);if [[ $round == 2 ]];then order=(f e3);fi
 for side in "${order[@]}";do
  code=0
  MUMENT_REUSE_SCALE=1 unshare --user --map-root-user --mount --propagation private "$out/$side-files-tests" -test.run '^TestContentCrossMountScale$' -test.v -test.count=1 > "$out/$side-$round.log" 2>&1 || code=$?
  printf '%s\n' "$code" > "$out/$side-$round.exit"
  [[ $code == 0 ]] || failed=1
 done
done
exit "$failed"
