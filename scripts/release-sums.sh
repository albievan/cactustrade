#!/bin/sh
# cactusTrade release (PLAN 7.9): write DIR/SHA256SUMS over the release binaries in DIR
# (cactustrade-<os>-<arch>[.exe]), in the `sha256sum` format setup.sh and setup.ps1 read.
#
#   scripts/release-sums.sh DIR
set -eu
dir="${1:?usage: release-sums.sh DIR}"
cd "$dir"
set -- cactustrade-*
[ -e "$1" ] || { echo "release-sums: no cactustrade-* binaries in $dir" >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then
	sha256sum "$@" >SHA256SUMS
else
	shasum -a 256 "$@" >SHA256SUMS
fi
echo "wrote $dir/SHA256SUMS ($# files)"
