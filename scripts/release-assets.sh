#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
VER="${1:-1.0.0}"
mkdir -p dist
for pair in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
  OS=${pair%/*}; ARCH=${pair#*/}
  EXT=""
  [[ "$OS" == windows ]] && EXT=".exe"
  out="dist/rehearsal-${OS}-${ARCH}${EXT}"
  echo "building $out"
  CGO_ENABLED=0 GOOS=$OS GOARCH=$ARCH go build -trimpath -ldflags="-s -w -X main.version=${VER}" -o "$out" ./cmd/rehearsal
  # operator binary (no windows for simplicity of manager process, still build for parity)
  opout="dist/rehearsal-operator-${OS}-${ARCH}${EXT}"
  echo "building $opout"
  CGO_ENABLED=0 GOOS=$OS GOARCH=$ARCH go build -trimpath -ldflags="-s -w" -o "$opout" ./cmd/rehearsal-operator
done
( cd dist && shasum -a 256 rehearsal-* rehearsal-operator-* > SHA256SUMS )
bash scripts/sbom.sh || true
echo "assets ready in dist/ for v${VER}"
