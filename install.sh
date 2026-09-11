#!/bin/sh
# Installs the latest otata release: https://github.com/jonathan-lor/otata
#
#   curl -fsSL https://raw.githubusercontent.com/jonathan-lor/otata/main/install.sh | sh
#
# It downloads the tarball for this machine, verifies it against the
# release's checksums, and puts the binary in ~/.local/bin (or $OTATA_BIN_DIR)
# by writing beside the target and renaming into place, so a server running
# the old binary keeps its image until it is restarted. A launch agent or
# systemd user unit that is installed is then refreshed, so that restart
# happens now rather than at the next login.
#
# On a Mac, `brew install --cask jonathan-lor/tap/otata` is the install that
# `brew upgrade` will track. This script works there too.
set -eu

repo="jonathan-lor/otata"
bin_dir="${OTATA_BIN_DIR:-$HOME/.local/bin}"
# The release to install: the latest, unless OTATA_VERSION names a tag.
# OTATA_RELEASE_URL points the download at somewhere other than GitHub,
# which is how the script is tested against a release that is not published.
version="${OTATA_VERSION:-}"
release_url="${OTATA_RELEASE_URL:-}"

fail() { echo "install.sh: $*" >&2; exit 1; }

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  linux|darwin) ;;
  *) fail "no release is built for $os; on Windows there is none, and elsewhere: go install github.com/$repo@latest" ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) fail "no release is built for $(uname -m)" ;;
esac

command -v curl >/dev/null || fail "curl is needed to download the release"
if command -v sha256sum >/dev/null; then
  checksum() { sha256sum "$1"; }
elif command -v shasum >/dev/null; then
  checksum() { shasum -a 256 "$1"; }
else
  fail "sha256sum or shasum is needed to verify the download"
fi

# The latest tag is where GitHub's releases/latest redirects, which needs
# neither the API nor its rate limit.
if [ -z "$version" ]; then
  location=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest") ||
    fail "could not look up the latest release"
  version="${location##*/}"
fi
case "$version" in
  v*) ;;
  *) fail "a release tag starts with v, not $version" ;;
esac
number="${version#v}"
[ -n "$release_url" ] || release_url="https://github.com/$repo/releases/download/$version"

archive="otata_${number}_${os}_${arch}.tar.gz"
sums="otata_${number}_checksums.txt"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "downloading otata $version for $os/$arch"
curl -fsSL -o "$tmp/$archive" "$release_url/$archive" || fail "no $archive in release $version"
curl -fsSL -o "$tmp/$sums" "$release_url/$sums" || fail "release $version has no checksums file"

# Verify before unpacking. The checksums file lists every archive; only the
# one downloaded is compared, by its own name.
expected=$(grep " $archive\$" "$tmp/$sums" | cut -d' ' -f1)
[ -n "$expected" ] || fail "$sums does not list $archive"
actual=$(cd "$tmp" && checksum "$archive" | cut -d' ' -f1)
[ "$actual" = "$expected" ] || fail "$archive does not match its checksum; not installing it"

tar -xzf "$tmp/$archive" -C "$tmp" otata
mkdir -p "$bin_dir"
mv "$tmp/otata" "$bin_dir/.otata.new"
chmod +x "$bin_dir/.otata.new"
mv "$bin_dir/.otata.new" "$bin_dir/otata"
echo "installed $bin_dir/otata ($("$bin_dir/otata" version 2>/dev/null || echo "$version"))"

# A unit that keeps the server alive is refreshed the way `make install`
# refreshes it: 'autostart on' reinstalls and restarts it on the new binary.
if [ -f "$HOME/Library/LaunchAgents/com.anakepha.otata.plist" ] ||
   [ -f "${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/otata.service" ]; then
  echo "refreshing the service so it runs the new binary"
  "$bin_dir/otata" autostart on >/dev/null 2>&1 || echo "could not refresh it; run 'otata autostart on'"
fi

case ":$PATH:" in
  *":$bin_dir:"*) ;;
  *) echo "add $bin_dir to your PATH to run otata by name" ;;
esac
