#!/bin/sh
set -eu

repository_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
fixture_root=$(mktemp -d "${TMPDIR:-/tmp}/mailman-installer-test.XXXXXX")
trap 'rm -rf "$fixture_root"' EXIT HUP INT TERM

for target in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64; do
  archive="mailman_${target}.tar.gz"
  payload="$fixture_root/payload-$target"
  mkdir -p "$payload"
  printf '#!/bin/sh\necho %s\n' "$target" > "$payload/mailman"
  chmod 0755 "$payload/mailman"
  tar -czf "$fixture_root/$archive" -C "$payload" mailman
done

: > "$fixture_root/checksums.txt"
for archive_path in "$fixture_root"/mailman_*.tar.gz; do
  archive_name=$(basename "$archive_path")
  if command -v sha256sum >/dev/null 2>&1; then
    checksum=$(sha256sum "$archive_path" | awk '{ print $1 }')
  else
    checksum=$(shasum -a 256 "$archive_path" | awk '{ print $1 }')
  fi
  printf '%s  %s\n' "$checksum" "$archive_name" >> "$fixture_root/checksums.txt"
done

for mapping in Darwin:x86_64:darwin_amd64 Darwin:arm64:darwin_arm64 Linux:x86_64:linux_amd64 Linux:aarch64:linux_arm64; do
  old_ifs=$IFS
  IFS=:
  set -- $mapping
  IFS=$old_ifs
  os_name=$1
  arch_name=$2
  expected=$3
  destination="$fixture_root/install-$expected"
  MAILMAN_OS="$os_name" \
  MAILMAN_ARCH="$arch_name" \
  MAILMAN_RELEASE_BASE_URL="file://$fixture_root" \
  MAILMAN_INSTALL_DIR="$destination" \
    sh "$repository_root/scripts/install.sh" >/dev/null
  actual=$("$destination/mailman")
  if [ "$actual" != "$expected" ]; then
    echo "installer mapping $mapping produced $actual" >&2
    exit 1
  fi
done

tampered_root="$fixture_root/tampered"
mkdir -p "$tampered_root"
cp "$fixture_root/mailman_linux_amd64.tar.gz" "$tampered_root/mailman_linux_amd64.tar.gz"
cp "$fixture_root/checksums.txt" "$tampered_root/checksums.txt"
printf 'tampered' >> "$tampered_root/mailman_linux_amd64.tar.gz"
if MAILMAN_OS=Linux \
  MAILMAN_ARCH=x86_64 \
  MAILMAN_RELEASE_BASE_URL="file://$tampered_root" \
  MAILMAN_INSTALL_DIR="$fixture_root/should-not-install" \
    sh "$repository_root/scripts/install.sh" >/dev/null 2>&1; then
  echo "installer accepted a tampered archive" >&2
  exit 1
fi

echo "installer platform and checksum tests passed"
