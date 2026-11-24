#!/bin/sh
# cactusTrade release (PLAN 7.9, OD31): sign DIR/SHA256SUMS with the release SSH key
# (`ssh-keygen -Y sign`, namespace cactustrade-release) into DIR/SHA256SUMS.sig. The private
# key never enters the repository; its public half is in scripts/allowed_signers, setup.sh
# and setup.ps1.
#
#   scripts/release-sign.sh KEY DIR
#
# A new key (the owner's, once):
#   ssh-keygen -t ed25519 -C cactustrade-release -f ~/.config/cactustrade/release_ed25519
#   then put  cactustrade-release namespaces="cactustrade-release" <the .pub line>
#   into scripts/allowed_signers and the SIGNER line of setup.sh and setup.ps1.
set -eu
key="${1:?usage: release-sign.sh KEY DIR}"
dir="${2:?usage: release-sign.sh KEY DIR}"
[ -f "$key" ] || {
	echo "release-sign: no release key at $key (restore it from your backup, or create one: see the top of this script)" >&2
	exit 1
}
[ -f "$dir/SHA256SUMS" ] || { echo "release-sign: no $dir/SHA256SUMS" >&2; exit 1; }
rm -f "$dir/SHA256SUMS.sig"
ssh-keygen -q -Y sign -f "$key" -n cactustrade-release "$dir/SHA256SUMS"
echo "signed $dir/SHA256SUMS -> $dir/SHA256SUMS.sig"
