# Rollout-failure demo

A two-line Kubernetes change passes kubeconform, kube-linter, conftest, kyverno and server-side dry-run, then takes the service to 0 ready endpoints on a real apply. `rehearsal argo-impact` predicts this and BLOCKs before the apply.

`run.sh` deploys a 3-replica `api` (PDB `minAvailable: 2`, readiness probes) into the namespace `checkout` on the current kubectl context, starts an in-cluster HTTP prober, and applies this change:

```diff
     type: RollingUpdate
+    rollingUpdate:
+      maxUnavailable: 100%   # "deploys take too long": roll all pods at once
+      maxSurge: 0
...
+          env:
+            - name: FEATURE_FLAGS
+              value: "fast-checkout=on"
```

## Recorded result (`transcript.txt`, rehearsal 1.8.2, kind, Kubernetes v1.33.1)

| Check | Result on the change |
|---|---|
| kubeconform -strict | 5/5 valid |
| kube-linter (defaults) | no lint errors |
| conftest, redhat-cop OCP best practices (130 tests) | 130 passed |
| kyverno CLI, 31 policies (PSS baseline/restricted + best practices) | pass 26, fail 0, error 0 |
| kubectl apply --dry-run=server | accepted |
| **rehearsal argo-impact --live** | **BLOCK (exit 3)**: `rollout can take available pods to 0; PodDisruptionBudgets do not protect against rollouts` |
| real `kubectl apply` | about 12 s at availableReplicas = 0 and 0 ready endpoints; 10 of 25 in-cluster HTTP probes failed |

The gate is read-only: the Deployment's resourceVersion and generation are the same before and after it runs. The outage length depends on image pull and probe timing.

## Run it

Prerequisites (versions used for the transcript): Docker, kind v0.29.0, kubectl v1.33.1, kubeconform v0.8.0, kube-linter 0.8.3, conftest 0.71.1, kyverno CLI 1.19.2, python3.

```
git clone https://github.com/justrunme/architecture-rehearsal
cd architecture-rehearsal/demo
kind create cluster --name rehearsal-demo
curl -fsSLo rehearsal https://github.com/justrunme/architecture-rehearsal/releases/download/v1.8.2/rehearsal-linux-amd64
chmod +x rehearsal    # on macOS use rehearsal-darwin-arm64 or rehearsal-darwin-amd64
REHEARSAL=$PWD/rehearsal ./run.sh
kind delete cluster --name rehearsal-demo
```

It takes about 1–2 minutes on a running cluster, plus image pulls on the first run. The script deletes the `checkout` namespace at the end.

## Fairness notes
- The manifests were adjusted until the baseline passes every tool: common labels, explicit NET_RAW drop, no CPU limit, explicit Service type, PDB `unhealthyPodEvictionPolicy`.
- Two upstream rules are not applied:
  - The conftest rule `container_volumemount_inconsistent_path`, which enforces an OpenShift `/var/run/<org>` path convention.
  - The kyverno `disallow-default-namespace` policy (in `policies/excluded/`). The offline CLI cannot evaluate `namespaceObject` and reports `error`; the manifests set `namespace: checkout` explicitly.
- None of these tools claims to model rollout availability. The demo shows the gap, not that the tools are bad.

Third-party policy attribution: see `NOTICE`.
