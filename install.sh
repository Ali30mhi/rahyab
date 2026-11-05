#!/bin/sh
# Installs the latest rahyab (or $RAHYAB_VERSION) for this OS/CPU and
# verifies its SHA-256 against the release's checksums.txt.
set -eu

repo="Ali30mhi/rahyab"

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) echo "unsupported OS $(uname -s); on Windows download the .exe from Releases" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "unsupported CPU $(uname -m)" >&2; exit 1 ;;
esac

# Resolve the tag first and download everything from that exact release:
# the /latest/download/ shortcut can serve the previous release for a few
# minutes after a new one, and the binary and checksums must match.
tag="${RAHYAB_VERSION:-}"
if [ -z "$tag" ]; then
  tag=$(curl -fsSI "https://github.com/$repo/releases/latest" | tr -d '\r' |
    awk 'tolower($1) == "location:" { n = split($2, p, "/"); print p[n] }')
fi
if [ -z "$tag" ]; then
  echo "couldn't find the latest release" >&2
  exit 1
fi

base="https://github.com/$repo/releases/download/$tag"
name="rahyab-$os-$arch"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "downloading rahyab $tag ($name)..."
curl -fL --retry 3 -o "$tmp/$name" "$base/$name"

if curl -fsL --retry 3 -o "$tmp/checksums.txt" "$base/checksums.txt"; then
  want=$(awk -v n="$name" '$2 == n || $2 == "*"n {print $1}' "$tmp/checksums.txt")
  if command -v sha256sum >/dev/null 2>&1; then
    got=$(sha256sum "$tmp/$name" | awk '{print $1}')
  else
    got=$(shasum -a 256 "$tmp/$name" | awk '{print $1}')
  fi
  if [ -z "$want" ] || [ "$want" != "$got" ]; then
    echo "checksum mismatch, not installing" >&2
    exit 1
  fi
  echo "checksum ok"
else
  echo "warning: this release has no checksums.txt, skipping verification" >&2
fi

chmod +x "$tmp/$name"
dest="${RAHYAB_DIR:-/usr/local/bin}"
if [ -w "$dest" ]; then
  mv "$tmp/$name" "$dest/rahyab"
else
  sudo mv "$tmp/$name" "$dest/rahyab"
fi
echo "installed to $dest/rahyab"
"$dest/rahyab" version
