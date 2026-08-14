#!/bin/sh
set -eu

repository="${DAGRAIL_REPOSITORY:-CongBao/dagrail}"
version="${DAGRAIL_VERSION:-latest}"
harness="${DAGRAIL_HARNESS:-codex,claude-code,copilot-cli}"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --harness) harness="$2"; shift 2 ;;
    --version) version="$2"; shift 2 ;;
    --help|-h) echo "usage: install.sh [--harness codex,claude-code,copilot-cli] [--version latest|vX.Y.Z]"; exit 0 ;;
    *) echo "dagrail: unknown option $1" >&2; exit 2 ;;
  esac
done

case "$(uname -s)" in Darwin) platform=darwin ;; Linux) platform=linux ;; *) echo "unsupported operating system" >&2; exit 1 ;; esac
case "$(uname -m)" in arm64|aarch64) architecture=arm64 ;; x86_64|amd64) architecture=amd64 ;; *) echo "unsupported architecture" >&2; exit 1 ;; esac

if [ "$version" = latest ]; then
  base="https://github.com/${repository}/releases/latest/download"
else
  case "$version" in v[0-9]*.[0-9]*.[0-9]*|v[0-9]*.[0-9]*.[0-9]*-*) ;; *) echo "version must be latest or v-prefixed" >&2; exit 2 ;; esac
  base="https://github.com/${repository}/releases/download/${version}"
fi

asset="dagrail_${platform}_${architecture}.tar.gz"
temporary="$(mktemp -d "${TMPDIR:-/tmp}/dagrail-install.XXXXXX")"
trap 'rm -rf "$temporary"' EXIT HUP INT TERM
curl --fail --location --proto '=https' --tlsv1.2 "${base}/${asset}" --output "${temporary}/${asset}"
curl --fail --location --proto '=https' --tlsv1.2 "${base}/checksums.txt" --output "${temporary}/checksums.txt"
expected="$(awk -v asset="$asset" '$2 == asset {count++; digest=$1} END {if (count == 1) print digest}' "${temporary}/checksums.txt")"
if command -v sha256sum >/dev/null 2>&1; then actual="$(sha256sum "${temporary}/${asset}" | awk '{print $1}')"; else actual="$(shasum -a 256 "${temporary}/${asset}" | awk '{print $1}')"; fi
[ -n "$expected" ] && [ "$expected" = "$actual" ] || { echo "checksum verification failed" >&2; exit 1; }
contents="$(tar -tzf "${temporary}/${asset}" | LC_ALL=C sort)"
expected_contents="$(printf '%s\n' LICENSE README.md dagrail | LC_ALL=C sort)"
[ "$contents" = "$expected_contents" ] || { echo "release archive contains unexpected paths" >&2; exit 1; }
tar -xzf "${temporary}/${asset}" -C "$temporary"
"${temporary}/dagrail" plugin install --harness "$harness"
"${temporary}/dagrail" plugin runtime-status >/dev/null
echo "DAGrail installed. Restart open agent applications."
