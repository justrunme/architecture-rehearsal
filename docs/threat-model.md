# Threat model (v0.12 / v1.0)

## Assets

- Baseline and observed architecture graphs
- Change envelopes and rendered manifests
- Impact reports and verification results
- Signed evidence / digest chain
- Cluster connection references
- Gate decisions (approve/warn/block/unknown)

## Adversaries

| Threat | Mitigation |
| ------ | ---------- |
| Baseline substitution | Content digests; chain bind report→baseline/change |
| Observed snapshot forgery | Digest + optional DSSE/Ed25519; require production verify mode |
| Replay of old report | Report digests bound to baseline/change; reject mismatch |
| Compromised runner | Non-root image; no inline kubeconfig secrets; short-lived tokens |
| Malicious manifests | Fail-closed YAML; no secret value collection |
| kubectl injection | Fixed resource list; no shell interpolation of user args into shell |
| Secret leakage | Collect never stores Secret data; only references |
| Forged evidence | HMAC/DSSE/Ed25519; document HMAC is not non-repudiation |
| Tenant escape | Org-scoped authz; path isolation; admin role explicit |

## Explicit non-goals (current)

- Full multi-tenant SaaS isolation with per-tenant KMS
- Sigstore keyless cosign in CI (documented integration path)
- Formal cryptographic proof of cluster state
