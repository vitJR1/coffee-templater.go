#!/usr/bin/env bash
# Updates the embedded engine (internal/wasm/coffee_core.wasm) from a GitHub Release of
# coffee-templater-engine, verifies its checksum and pins version and sha256 in internal/wasm/wasm.go.
#
#   scripts/update-engine.sh 0.1.3                      # release v0.1.3
#   scripts/update-engine.sh --local ../coffee-templater-engine/dist/coffee_core.wasm
#
# The conformance cases are copied as well when the engine checkout is next to this repository
# (or ENGINE_DIR points to it). Run `go test ./...` afterwards.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
REPO="vitJR1/coffee-templater-engine"
OUT="$ROOT/internal/wasm/coffee_core.wasm"
PIN="$ROOT/internal/wasm/wasm.go"
ENGINE_DIR="${ENGINE_DIR:-$ROOT/../coffee-templater-engine}"

sha256() { if command -v sha256sum >/dev/null; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

if [[ "${1:-}" == "--local" ]]; then
  src="${2:?path to coffee_core.wasm}"
  cp "$src" "$tmp/coffee_core.wasm"
  version="$(sed -n 's/^const Version = "\(.*\)"/\1/p' "$PIN")-local"
else
  version="${1:?usage: update-engine.sh <version> | --local <path>}"
  version="${version#v}"
  base="https://github.com/$REPO/releases/download/v$version"
  curl -fsSL --retry 3 -o "$tmp/coffee_core.wasm" "$base/coffee_core.wasm"
  curl -fsSL --retry 3 -o "$tmp/coffee_core.wasm.sha256" "$base/coffee_core.wasm.sha256"
  expected="$(cut -d' ' -f1 "$tmp/coffee_core.wasm.sha256")"
  actual="$(sha256 "$tmp/coffee_core.wasm")"
  if [[ "$expected" != "$actual" ]]; then
    echo "checksum mismatch: release says $expected, downloaded $actual" >&2
    exit 1
  fi
fi

if [[ "$(head -c 4 "$tmp/coffee_core.wasm" | od -An -tx1 | tr -d ' \n')" != "0061736d" ]]; then
  echo "not a WebAssembly binary" >&2
  exit 1
fi

sum="$(sha256 "$tmp/coffee_core.wasm")"
cp "$tmp/coffee_core.wasm" "$OUT"
sed -i.bak -e "s/^const Version = \".*\"/const Version = \"$version\"/" \
           -e "s/^const SHA256 = \".*\"/const SHA256 = \"$sum\"/" "$PIN"
rm -f "$PIN.bak"
echo "engine $version ($sum)"

if [[ -d "$ENGINE_DIR/conformance/cases" ]]; then
  rm -rf "$ROOT/testdata/conformance"
  mkdir -p "$ROOT/testdata/conformance"
  cp -R "$ENGINE_DIR/conformance/cases" "$ENGINE_DIR/conformance/fixtures" "$ROOT/testdata/conformance/"
  rm -f "$ROOT"/testdata/conformance/fixtures/*.py
  echo "conformance cases copied from $ENGINE_DIR (check that its checkout matches v$version)"
fi
