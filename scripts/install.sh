#!/bin/sh
set -eu

repository="${MAILMAN_REPOSITORY:-nmhossain02/mailman}"
release_version="${MAILMAN_VERSION:-latest}"
install_dir="${MAILMAN_INSTALL_DIR:-$HOME/.local/bin}"
detected_os="${MAILMAN_OS:-$(uname -s)}"
detected_arch="${MAILMAN_ARCH:-$(uname -m)}"

case "$detected_os" in
  Darwin|darwin) target_os="darwin" ;;
  Linux|linux) target_os="linux" ;;
  *) echo "mailman: unsupported operating system: $detected_os" >&2; exit 1 ;;
esac

case "$detected_arch" in
  x86_64|amd64) target_arch="amd64" ;;
  arm64|aarch64) target_arch="arm64" ;;
  *) echo "mailman: unsupported architecture: $detected_arch" >&2; exit 1 ;;
esac

archive="mailman_${target_os}_${target_arch}.tar.gz"
if [ -n "${MAILMAN_RELEASE_BASE_URL:-}" ]; then
  release_base="$MAILMAN_RELEASE_BASE_URL"
elif [ "$release_version" = "latest" ]; then
  release_base="https://github.com/${repository}/releases/latest/download"
else
  release_base="https://github.com/${repository}/releases/download/${release_version}"
fi

temporary_dir=$(mktemp -d "${TMPDIR:-/tmp}/mailman-install.XXXXXX")
trap 'rm -rf "$temporary_dir"' EXIT HUP INT TERM

download() {
  source_url="$1"
  destination="$2"
  case "$source_url" in
    https://*) curl --proto '=https' --tlsv1.2 -fsSL "$source_url" -o "$destination" ;;
    file://*) curl -fsSL "$source_url" -o "$destination" ;;
    *) echo "mailman: refusing non-HTTPS release URL: $source_url" >&2; exit 1 ;;
  esac
}

download "$release_base/$archive" "$temporary_dir/$archive"
download "$release_base/checksums.txt" "$temporary_dir/checksums.txt"

expected=$(awk -v file="$archive" '$2 == file { print $1 }' "$temporary_dir/checksums.txt")
if [ -z "$expected" ]; then
  echo "mailman: checksum entry missing for $archive" >&2
  exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$temporary_dir/$archive" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$temporary_dir/$archive" | awk '{ print $1 }')
else
  echo "mailman: sha256sum or shasum is required" >&2
  exit 1
fi

if [ "$actual" != "$expected" ]; then
  echo "mailman: checksum verification failed for $archive" >&2
  exit 1
fi

tar -xzf "$temporary_dir/$archive" -C "$temporary_dir" mailman
mkdir -p "$install_dir"
install -m 0755 "$temporary_dir/mailman" "$install_dir/mailman"

echo "Installed mailman to $install_dir/mailman"
"$install_dir/mailman" version
case ":$PATH:" in
  *":$install_dir:"*) ;;
  *) echo "Add $install_dir to PATH to run mailman from any directory." ;;
esac
