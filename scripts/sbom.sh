#!/usr/bin/env bash
# Generate SBOM + checksums for release (v1.0 production contract).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
mkdir -p dist
go version
# CycloneDX via go list if syft/cyclonedx-gomod unavailable
if command -v syft >/dev/null 2>&1; then
  syft dir:. -o cyclonedx-json > dist/sbom.cdx.json
elif command -v cyclonedx-gomod >/dev/null 2>&1; then
  cyclonedx-gomod mod -json -output dist/sbom.cdx.json
else
  go list -m -json all > dist/sbom.gomod.json
  echo "wrote dist/sbom.gomod.json (install syft for CycloneDX)"
fi
echo "SBOM artifacts in dist/"
