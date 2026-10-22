#!/usr/bin/env bash
# C5 demo: a change that passes kubeconform, kube-linter, conftest, kyverno and
# server-side dry-run, but takes the service to 0 ready pods during rollout.
# Requires: a kind cluster (current kubectl context), kubeconform, kube-linter,
# conftest, kyverno, and the `rehearsal` binary (REHEARSAL=path).
set -uo pipefail
cd "$(dirname "$0")"
R=${REHEARSAL:-rehearsal}
NS=checkout
step(){ echo; echo "### $*"; }
die(){ echo "ERROR: $*" >&2; echo "Hint: check image pulls with: kubectl -n $NS get events --sort-by=.lastTimestamp" >&2; kubectl delete ns $NS --ignore-not-found --wait >/dev/null; exit 1; }
run(){ echo "\$ $*"; "$@"; echo "[exit $?]"; }

step "0. Versions"
kubectl version 2>/dev/null | grep -i server; kubeconform -v; kube-linter version; conftest --version | head -1; kyverno version 2>/dev/null | grep -i version | head -1; $R version | head -1

step "1. Deploy baseline (3 replicas, PDB minAvailable=2) and start an in-cluster prober"
kubectl delete ns $NS --ignore-not-found --wait >/dev/null
kubectl apply -f manifests/baseline.yaml
kubectl -n $NS rollout status deploy/api --timeout=180s || die "baseline Deployment did not become ready; results would be meaningless"
kubectl -n $NS run prober --image=nginxinc/nginx-unprivileged:1.27-alpine --restart=Never \
  --overrides='{"spec":{"securityContext":{"runAsNonRoot":true,"runAsUser":101,"seccompProfile":{"type":"RuntimeDefault"}},"containers":[{"name":"prober","image":"nginxinc/nginx-unprivileged:1.27-alpine","command":["sh","-c","while true; do if wget -q -T 1 -O /dev/null http://api.checkout.svc/; then echo \"$(date +%T.%N | cut -c1-12) OK\"; else echo \"$(date +%T.%N | cut -c1-12) FAIL\"; fi; sleep 0.2; done"],"securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}}}]}}' >/dev/null
kubectl -n $NS wait --for=condition=Ready pod/prober --timeout=120s >/dev/null || die "prober pod did not become ready"; sleep 3
echo "prober before change: $(kubectl -n $NS logs prober | grep -E ' (OK|FAIL)$' | awk '{print $2}' | sort | uniq -c | tr '\n' ' ')"

step "2. The change (diff)"
cat manifests/change.diff

step "3. Standard checks on the changed manifests"
run kubeconform -strict -summary manifests/change.yaml
run kube-linter lint manifests/change.yaml
run conftest test --rego-version v0 --no-color --policy policies/conftest/bestpractices --policy policies/conftest/lib --all-namespaces manifests/change.yaml
echo "(kyverno: 31 policies from lab corpus; disallow-default-namespace moved to policies/excluded/ - offline CLI cannot evaluate namespaceObject; manifests set namespace: checkout explicitly)"
run kyverno apply policies/kyverno/ --resource manifests/change.yaml
run kubectl apply --dry-run=server -f manifests/change.yaml

step "4. Change-impact gate against the live cluster (read-only)"
before=$(kubectl -n $NS get deploy api -o jsonpath='rv={.metadata.resourceVersion} gen={.metadata.generation}')
run $R argo-impact --live --desired-dir desired --namespace $NS --app $NS --out out/gate --quiet
echo "--- gate report (scheduling/rollout detectors)"; sed -n '1,40p' out/gate/argo-impact.md
echo "deployment before gate: $before  after gate: $(kubectl -n $NS get deploy api -o jsonpath='rv={.metadata.resourceVersion} gen={.metadata.generation}')"

step "5. Apply it for real and watch availability"
start=$(kubectl -n $NS logs prober | grep -cE ' (OK|FAIL)$')
kubectl apply -f manifests/change.yaml
min=99
for i in $(seq 1 60); do
  a=$(kubectl -n $NS get deploy api -o jsonpath='{.status.availableReplicas}'); a=${a:-0}
  r=$(kubectl -n $NS get endpointslices -l kubernetes.io/service-name=api -o json | python3 -c 'import json,sys; print(sum(1 for i in json.load(sys.stdin)["items"] for e in (i.get("endpoints") or []) if e.get("conditions",{}).get("ready")))')
  printf '%s availableReplicas=%s readyEndpoints=%s\n' "$(date +%T.%N | cut -c1-12)" "$a" "$r"
  [ "$a" -lt "$min" ] && min=$a
  [ $i -gt 6 ] && [ "$a" = 3 ] && break
  sleep 0.5
done
kubectl -n $NS rollout status deploy/api --timeout=120s; sleep 2
echo "--- prober during rollout (in-cluster wget http://api every 0.2s, 1s timeout; prober clock is UTC)"
kubectl -n $NS logs prober | grep -E ' (OK|FAIL)$' | tail -n +$((start+1)) | awk '{c[$2]++} $2=="FAIL"{if(!f)f=$1; l=$1} END{for(k in c) printf "%s=%d ", k, c[k]; if(f) printf "| first FAIL %s, last FAIL %s", f, l; print ""}'
echo "minimum availableReplicas observed: $min (PDB minAvailable=2)"

step "6. Cleanup"
kubectl delete ns $NS --wait >/dev/null && echo "namespace $NS deleted"
