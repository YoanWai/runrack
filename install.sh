#!/bin/sh
# Install runrack from GitHub Releases. No sudo.
#   curl -fsSL https://raw.githubusercontent.com/YoanWai/runrack/main/install.sh | sh
# RUNRACK_VERSION picks a release (default: latest), RUNRACK_INSTALL_DIR the target (default: ~/.local/bin),
# RUNRACK_DOWNLOAD_URL a mirror of the release files.
set -eu

repo="YoanWai/runrack"
base_url="${RUNRACK_DOWNLOAD_URL:-https://github.com/$repo/releases/download}"
dir="${RUNRACK_INSTALL_DIR:-$HOME/.local/bin}"

fail() {
	echo "runrack install: $*" >&2
	exit 1
}

[ "$(uname -s)" = Linux ] || fail "runrack runs on Linux only for now"
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) fail "no build for $(uname -m)" ;;
esac
command -v curl >/dev/null || fail "curl is required"
command -v sha256sum >/dev/null || fail "sha256sum is required"

version="${RUNRACK_VERSION:-}"
if [ -z "$version" ]; then
	version=$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest" |
		sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
	[ -n "$version" ] || fail "could not find the latest release"
fi
version="${version#v}"

archive="runrack_${version}_linux_${arch}.tar.gz"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "downloading runrack $version for linux/$arch"
curl -fsSL -o "$tmp/$archive" "$base_url/v$version/$archive"
curl -fsSL -o "$tmp/checksums.txt" "$base_url/v$version/checksums.txt"
(cd "$tmp" && grep "  $archive\$" checksums.txt | sha256sum -c --quiet -) || fail "checksum mismatch for $archive"

tar -xzf "$tmp/$archive" -C "$tmp" runrack
mkdir -p "$dir"
install -m 0755 "$tmp/runrack" "$dir/runrack"
echo "installed $dir/runrack"

case ":$PATH:" in
*":$dir:"*) ;;
*) echo "note: $dir is not on your PATH. Add it, or run $dir/runrack" ;;
esac
