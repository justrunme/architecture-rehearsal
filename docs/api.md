# Control plane API (v1)

Base URL: `http://localhost:8080`

Auth (v1.0.1): `Authorization: Bearer <token>`.

- Set `REHEARSAL_API_TOKEN` (required to start `serve` in production).
- Optional multi-token map: `REHEARSAL_API_TOKENS` JSON.
- Local only: `rehearsal serve --insecure-dev` enables `local-dev`.
- **Hardcoded `ci` / `viewer-token` removed.**
- Client `X-Org` does **not** change principal org; tenant is bound to the token.
- Object reads use labels of the stored object (cross-tenant → 404).

| Method | Path | Action |
| ------ | ---- | ------ |
| GET | /healthz | Liveness |
| GET | /readyz | Readiness |
| GET | /v1/version | Version |
| GET | /v1/schemas | Contract catalog |
| POST | /v1/runs | Create run (idempotencyKey supported) |
| GET | /v1/runs | List runs |
| GET | /v1/runs/{id} | Get run |
| POST | /v1/runs/{id}/advance | Execute lifecycle or cancel |
| GET | /v1/runs/{id}/evidence | Digests |
| POST | /v1/clusters | Register cluster (secret ref only) |
| GET | /v1/clusters | List clusters |
| POST | /v1/policies | Store policy document |
| GET | /v1/calibration | Scenario quality report |

All mutations should send `idempotencyKey` for safe retries.
