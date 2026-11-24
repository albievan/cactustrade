#!/bin/sh
# cactusTrade setup: downloads the newest release of the cactustrade program for this
# computer (Linux or macOS, Intel/AMD or ARM 64-bit) into the current folder, checks it, and
# replaces an older cactustrade there (kept as cactustrade.old). Run it in the folder
# cactustrade lives in:
#
#   curl -fsSL https://raw.githubusercontent.com/albievan/cactustrade/main/setup.sh | sh
#
# Set CACTUSTRADE_VERSION=1.2.0 to fetch that release instead of the newest one.
#
# Nothing is installed unless SHA256SUMS carries the release signature (ssh-keygen -Y, PLAN
# 7.9, OD31) of the key below AND the program's checksum is in it. That needs OpenSSH 8.1 or
# newer (`ssh-keygen -Y`), which current macOS and Linux have.
set -eu

repo="https://github.com/albievan/cactustrade/releases"
# The public half of the release key: the same line as scripts/allowed_signers (a test checks
# it). Empty until the owner publishes the key: then this script refuses to install.
SIGNER=''

fail() { echo "cactustrade setup: $*" >&2; exit 1; }

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) fail "this script is for Linux and macOS; on Windows use setup.ps1 (see the README)" ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) fail "no release for the $(uname -m) processor (there are amd64 and arm64)" ;;
esac
if [ "$os" = darwin ] && [ "$arch" = amd64 ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
	arch=arm64 # an Intel shell under Rosetta on Apple Silicon: take the native program
fi
asset="cactustrade-$os-$arch"
if [ -n "${CACTUSTRADE_RELEASE_URL:-}" ]; then
	base="$CACTUSTRADE_RELEASE_URL" # a mirror or a local folder (file://…), checked the same way
elif [ -n "${CACTUSTRADE_VERSION:-}" ]; then
	base="$repo/download/v$CACTUSTRADE_VERSION"
else
	base="$repo/latest/download"
fi

command -v curl >/dev/null 2>&1 || fail "curl is needed"
command -v ssh-keygen >/dev/null 2>&1 || fail "ssh-keygen (OpenSSH 8.1 or newer) is needed to check the release signature"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

if [ -n "${CACTUSTRADE_RELEASE_SIGNERS:-}" ]; then
	echo "WARNING: trusting the release key(s) in $CACTUSTRADE_RELEASE_SIGNERS instead of cactusTrade's own." >&2
	cp "$CACTUSTRADE_RELEASE_SIGNERS" "$tmp/allowed_signers" || fail "cannot read $CACTUSTRADE_RELEASE_SIGNERS"
else
	[ -n "$SIGNER" ] || fail "no release key is published in this setup script yet: nothing can be checked, so nothing is installed (build from source: make build)"
	printf '%s\n' "$SIGNER" >"$tmp/allowed_signers"
fi

echo "Downloading $asset ..."
curl -fL --progress-bar -o "$tmp/$asset" "$base/$asset" || fail "download of $base/$asset failed"
curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS" || fail "download of SHA256SUMS failed"
curl -fsSL -o "$tmp/SHA256SUMS.sig" "$base/SHA256SUMS.sig" || fail "download of SHA256SUMS.sig failed (an unsigned release is never installed)"

ssh-keygen -Y verify -f "$tmp/allowed_signers" -I cactustrade-release -n cactustrade-release \
	-s "$tmp/SHA256SUMS.sig" <"$tmp/SHA256SUMS" >/dev/null 2>&1 ||
	fail "the release signature does not match (or this ssh-keygen has no -Y verify): not installing"
echo "Release signature: valid."

want="$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1 }' "$tmp/SHA256SUMS")"
[ -n "$want" ] || fail "$asset is not in SHA256SUMS"
if command -v sha256sum >/dev/null 2>&1; then
	got="$(sha256sum "$tmp/$asset" | awk '{ print $1 }')"
else
	got="$(shasum -a 256 "$tmp/$asset" | awk '{ print $1 }')"
fi
[ "$got" = "$want" ] || fail "the checksum of $asset does not match: not installing"
echo "Checksum: matches."

chmod +x "$tmp/$asset"
new="$("$tmp/$asset" version)" || fail "the downloaded program does not run on this computer"
if [ -f cactustrade ]; then
	mv -f cactustrade cactustrade.old
	echo "Kept the previous program as cactustrade.old ($(./cactustrade.old version 2>/dev/null || echo unknown version))."
fi
mv "$tmp/$asset" cactustrade
echo "Installed cactustrade $new in $(pwd)."
echo "Stop cactustrade if it was running and start it again: ./cactustrade (https://localhost:8443)."
echo "A first start creates ./data with the SQLite database and master.key (back that key up),"
echo "and the administrator admin/admin, who must choose a new password. Accounts trade on eToro's"
echo "demo by default. See https://github.com/albievan/cactustrade#quick-start-standalone-sqlite"
