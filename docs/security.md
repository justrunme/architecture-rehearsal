# Security

**See also:** [threat-model.md](threat-model.md) · [operator-security.md](operator-security.md)

## Principles

1. **Fail closed** — missing coverage → `unknown`, never silent approve.
2. **No secret values in the graph** — collectors store refs only.
3. **Evidence is digest-bound** — baseline / change / report / chain.
4. **Operator cannot be redirected by CR users** — URL/token from Deployment only.

## Threat summary

| Threat | Mitigation |
| ------ | ---------- |
| Secret exfiltration via collector | Secret **values** never collected; only refs |
| Untrusted baseline → false approve | Validation + `unknown` when coverage missing |
| Supply-chain compromise of binary | Distroless non-root image; SBOM in GitHub Release |
| Evidence tampering | SHA-256 digests; optional DSSE (HMAC / Ed25519) |
| Privilege escalation via Rehearsal | CLI is analysis-only — **no deploy, no mutate** |
| Token theft via CR | No API URL on Spec (v1.5.1+) |

## Collector policy

- Read-only inputs (manifests / plan JSON / optional GET)
- No Secret data, no env values, no ConfigMap payloads by default
- Annotations matching `token|password|secret|key` should be redacted by importers

## CI permissions

GitHub Actions workflows use least privilege (`contents: read` for CI; `packages: write` only on Release).
