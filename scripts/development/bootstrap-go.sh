#!/usr/bin/env bash
# Download a verified, repository-local Go toolchain; never modify system Go.
set -euo pipefail
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
tools_dir="$repo/.local-tools"
for tool in curl python3 sha256sum tar; do
  command -v "$tool" >/dev/null || { echo "Missing prerequisite: $tool" >&2; exit 1; }
done
case "$(uname -s)/$(uname -m)" in
  Linux/x86_64) platform=linux-amd64 ;;
  Linux/aarch64|Linux/arm64) platform=linux-arm64 ;;
  *) echo 'This bootstrap supports Linux amd64 and arm64.' >&2; exit 1 ;;
esac
mkdir -p "$tools_dir"
if [[ -e "$tools_dir/go" ]]; then
  echo "Refusing to replace existing $tools_dir/go; inspect it first." >&2
  exit 1
fi
stage=$(mktemp -d "$tools_dir/.go-download.XXXXXX")
trap 'rm -rf -- "$stage"' EXIT
curl --fail --location --retry 3 --connect-timeout 20 --max-time 180 \
  'https://go.dev/dl/?mode=json&include=all' -o "$stage/releases.json"
python3 - "$repo/backend/go.mod" "$stage/releases.json" "$platform" > "$stage/selection" <<'PY'
import json, re, sys
from pathlib import Path
required = re.search(r'^go (\d+)\.(\d+)\.(\d+)\s*$', Path(sys.argv[1]).read_text(), re.M)
if required is None:
    sys.exit('Cannot parse go.mod Go version')
minimum = tuple(map(int, required.groups()))
os_name, arch = sys.argv[3].split('-')
candidates = []
for release in json.loads(Path(sys.argv[2]).read_text()):
    match = re.fullmatch(r'go(\d+)\.(\d+)\.(\d+)', release['version'])
    if not match or not release.get('stable'):
        continue
    version = tuple(map(int, match.groups()))
    # Stay on the repository's required minor, selecting its newest patch.
    if version[:2] != minimum[:2] or version < minimum:
        continue
    for item in release['files']:
        if item['os'] == os_name and item['arch'] == arch and item['kind'] == 'archive':
            candidates.append((version, item))
if not candidates:
    sys.exit('Official manifest has no stable archive for the required Go minor; do not lower go.mod')
_, chosen = max(candidates, key=lambda entry: entry[0])
filename, checksum = chosen['filename'], chosen['sha256']
if not re.fullmatch(r'go\d+\.\d+\.\d+\.linux-(amd64|arm64)\.tar\.gz', filename):
    sys.exit('Unexpected archive filename')
if not re.fullmatch(r'[a-f0-9]{64}', checksum):
    sys.exit('Unexpected checksum')
print(filename)
print(checksum)
PY
mapfile -t selection < "$stage/selection"
archive=${selection[0]}
checksum=${selection[1]}
curl --fail --location --retry 3 --connect-timeout 20 --max-time 1800 \
  "https://go.dev/dl/$archive" -o "$stage/$archive"
printf '%s  %s\n' "$checksum" "$stage/$archive" | sha256sum --check -
tar -xzf "$stage/$archive" -C "$stage"
"$stage/go/bin/go" version
mv -T -- "$stage/go" "$tools_dir/go"
printf 'Installed verified %s\nAdd to PATH: %s/go/bin\n' "$archive" "$tools_dir"
