#!/usr/bin/env bash
# v0.4 iron path: kubectl List dump → graph → scoped change → analyze → verify
# Expected: analyze BLOCK (CNI), verify VERIFIED against observed dump + meta.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
BIN="${BIN:-bin/rehearsal}"
if [[ ! -x "$BIN" ]]; then
  go build -o "$BIN" ./cmd/rehearsal
fi

OUT="${E2E_OUT:-out/e2e-pipeline}"
rm -rf "$OUT"
mkdir -p "$OUT"

FIX=examples/e2e-pipeline
echo "==> 1/5 snapshot k8s (baseline from kubectl List dump)"
"$BIN" snapshot k8s \
  --dir "$FIX/cluster-dump" \
  --cluster acme-prod \
  --phase baseline \
  --out "$OUT/baseline.json"

echo "==> 2/5 change manifests (scoped to payments; removals off)"
"$BIN" change manifests \
  --baseline "$OUT/baseline.json" \
  --dir "$FIX/rendered-chart" \
  --namespace payments \
  --id change-helm-scale-payments-e2e \
  --title "Helm upgrade: scale payments stack (e2e)" \
  --out "$OUT/change.json"

# Scoped change must not mass-delete gitops
if command -v python3 >/dev/null 2>&1; then
  python3 - <<'PY' "$OUT/change.json"
import json, sys
ch = json.load(open(sys.argv[1]))
removed = ch.get("removedNodes") or []
assert not removed, f"unexpected removals without --allow-remove: {removed}"
patches = ch.get("patchNodes") or []
assert patches, "expected patchNodes for replica scale"
for p in patches:
    assert p["id"].startswith("workload/payments/"), p
print(f"  scope OK: {len(patches)} patch(es), 0 removals")
PY
fi

echo "==> 3/5 analyze (expect exit 3 block)"
set +e
"$BIN" analyze \
  --baseline "$OUT/baseline.json" \
  --change "$OUT/change.json" \
  --out "$OUT" \
  --html "$OUT/report.html" \
  --quiet
code=$?
set -e
if [[ "$code" -ne 3 ]]; then
  echo "error: analyze expected exit 3 (block), got $code" >&2
  if [[ -f "$OUT/latest-report.json" ]]; then
    cat "$OUT/latest-report.json" >&2
  fi
  exit 1
fi
echo "  analyze blocked as expected"

REPORT="$OUT/latest-report.json"
if [[ ! -f "$REPORT" ]]; then
  echo "error: missing $REPORT" >&2
  exit 1
fi

echo "==> 4/5 snapshot k8s (observed post-deploy dump + meta)"
"$BIN" snapshot k8s \
  --dir "$FIX/observed-dump" \
  --cluster acme-prod \
  --phase observed \
  --meta "$FIX/observed-meta.json" \
  --out "$OUT/observed.json"

echo "==> 5/5 verify (expect exit 0 verified; independent of annotation alone)"
set +e
"$BIN" verify \
  --report "$REPORT" \
  --observed "$OUT/observed.json" \
  --baseline "$OUT/baseline.json" \
  --change "$OUT/change.json" \
  --out "$OUT/verify.json"
vcode=$?
set -e
if [[ "$vcode" -ne 0 ]]; then
  echo "error: verify expected exit 0 (verified), got $vcode" >&2
  cat "$OUT/verify.json" >&2 || true
  exit 1
fi

if command -v python3 >/dev/null 2>&1; then
  python3 - <<'PY' "$OUT/verify.json" "$REPORT"
import json, sys
v = json.load(open(sys.argv[1]))
r = json.load(open(sys.argv[2]))
assert v.get("outcome") == "verified", v
assert r.get("decision") == "block", r
assert "cni-ip-capacity" in (r.get("predicted_failures") or []), r
assert v.get("deployedChangeDigest"), "missing deployedChangeDigest"
# independent scenario check must have passed (not soft annotation only)
names = [c["name"] for c in v.get("checks") or [] if c.get("passed") and not c.get("soft")]
assert any(n.startswith("scenario:cni-ip-capacity") for n in names), names
print(f"  verify OK outcome={v['outcome']} score={v.get('score')} digest={v.get('deployedChangeDigest')}")
print(f"  decision={r['decision']} risk={r['risk']} failures={r.get('predicted_failures')}")
PY
fi

echo "e2e pipeline OK: dump→graph→scoped change→block→verify"
echo "artifacts: $OUT"
