# Security

## Threat model (v1.0)

| Threat | Mitigation |
| ------ | ---------- |
| Secret exfiltration via collector | Secret **values** never collected; only refs |
| Untrusted baseline → false approve | Validation + `unknown` when coverage missing |
| Supply-chain compromise of binary | Distroless non-root image; SBOM via release process |
| Evidence tampering | SHA-256 in evidence-manifest.json |
| Privilege escalation via Rehearsal | CLI is analysis-only; **no deploy, no mutate** |

## Collector policy

- Read-only inputs (manifests / plan JSON / optional future kubeconfig GET)
- No Secret data, no env values, no ConfigMap payloads by default
- Annotations matching `token|password|secret|key` should be redacted by importers

## CI permissions

GitHub Actions examples use `permissions: contents: read` only.
