# Evidence

Maps rehearsal/gate claims to CI proof. Status vocabulary: **Tested in CI** /
**Reference implementation** / **Simulated** / **Not yet**.

There is no `docs/results.md` with published wall-clock SLOs; do not invent timings.

## Claim table

| Claim | Status | Scenario + environment | Real limits | Proof link | Negative tests |
| --- | --- | --- | --- | --- | --- |
| Deterministic policy gate (approve/warn/block/unknown) on fixture pipeline | Tested in CI | Job **test**: `go test ./...`, Demo (expect block), `scripts/e2e_pipeline.sh`, verify-loop golden multi-scenario | Offline cluster dumps / fixtures under `examples/e2e-pipeline` — blast radius is **simulated** from graph+change, not chaos against a live prod cluster | Latest green CI: [38118962080](https://github.com/justrunme/architecture-rehearsal/actions/runs/38118962080) · SHA `76a72b9c8194467edac96e726138cd47c228f9bc` · job test | Demo expects **block** (non-zero/exit contract); e2e pipeline asserts block then verify |
| Content-addressed evidence chain + DSSE sign/verify path | Tested in CI | Covered by demo/e2e/verify steps invoking `rehearsal evidence` flows | HMAC/DSSE keys are CI/ephemeral unless operator-provided | Same CI run · job test | Verify fails when chain/envelope mismatch |
| Operator Helm install on kind | Tested in CI | Job **operator-kind-e2e**; kind **v0.27.0**; `scripts/kind-operator-e2e.sh` | Single-node kind; not multi-cluster fleet | Same CI run · job operator-kind-e2e | Helm/operator asserts fail the job |
| Postgres persist contract (Open/CreateRun/ClaimJob) | Tested in CI | Job **postgres** → `go test ./internal/persist/` | Contract tests — not a managed Postgres HA soak | Same CI run · job postgres | Persist tests fail CI |
| Native Argo CD change-impact gate (`rehearsal argo-impact`) | Tested in CI | Job **argo-kind-e2e**; kind **v0.27.0**; `scripts/kind-argo-impact-e2e.sh` — fixture apps + **native Argo CD Applications** (Synced guestbook PASS; OutOfSync risky Deployment BLOCK); CLIFetcher via `argocd app manifests` (`facts.source=argocd-cli`); offline `scripts/e2e_argo_impact.sh` in job **test** | Single-node kind + in-cluster Argo CD — not a multi-cluster prod control plane | PR [#42](https://github.com/justrunme/architecture-rehearsal/pull/42) / main CI: [38133353467](https://github.com/justrunme/architecture-rehearsal/actions/runs/38133353467) · SHA `7383dca92c425fba57d4a25608ad88f348bcf046` · job [argo-kind-e2e](https://github.com/justrunme/architecture-rehearsal/actions/runs/38133353467/job/114452072077); PR-event confirmation: [38132879905](https://github.com/justrunme/architecture-rehearsal/actions/runs/38132879905) | Risky app must exit **3** (BLOCK); benign Synced must exit **0**; empty/`UNKNOWN` paths fail closed |
| Live kube-apiserver collect against arbitrary prod clusters | Reference / Not yet | `rehearsal snapshot k8s` supports dumps; CI uses fixtures + kind operator path | No published multi-tenant prod collect evidence file | — | Missing data must yield **unknown**/non-approve (product rule) — covered in unit/e2e expectations |
| Production change-management SLOs (time-to-gate, false-approve rate) | Not yet | No numbers in-repo results table | — | — | — |

## Not proven / out of scope

- Guaranteeing zero false approves on incomplete real-world inventories without operator-provided data quality
- Multi-cluster federation of evidence stores
- Equating simulated blast-radius graphs with live fault injection

## How to reproduce

```bash
make demo && make e2e
make build && ./bin/rehearsal version   # expects 1.5.4 line
bash scripts/e2e_pipeline.sh
bash scripts/kind-operator-e2e.sh
bash scripts/kind-argo-impact-e2e.sh   # requires kind + Argo CD
```
