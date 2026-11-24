#!/bin/sh
# cactusTrade release (PLAN 7.9, OD31): check a release folder the way setup.sh does: the
# signature of SHA256SUMS against the allowed signers (default scripts/allowed_signers), then
# every binary's checksum. Exit 0 only when both hold.
#
#   scripts/release-verify.sh DIR [ALLOWED_SIGNERS]
set -eu
dir="${1:?usage: release-verify.sh DIR [ALLOWED_SIGNERS]}"
signers="${2:-$(dirname "$0")/allowed_signers}"
grep -q '^cactustrade-release ' "$signers" 2>/dev/null || {
	echo "release-verify: $signers has no cactustrade-release key (the owner adds it: scripts/release-sign.sh)" >&2
	exit 1
}
ssh-keygen -Y verify -f "$signers" -I cactustrade-release -n cactustrade-release \
	-s "$dir/SHA256SUMS.sig" <"$dir/SHA256SUMS" >/dev/null || {
	echo "release-verify: the signature of $dir/SHA256SUMS does not verify" >&2
	exit 1
}
cd "$dir"
if command -v sha256sum >/dev/null 2>&1; then
	sha256sum -c --quiet SHA256SUMS
else
	shasum -a 256 -c --quiet SHA256SUMS
fi
echo "release-verify: signature and checksums of $dir are good"
