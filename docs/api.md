# Control plane API (v1)

Base URL: `http://localhost:8080`

Auth: `Authorization: Bearer <token>` (`REHEARSAL_API_TOKEN`, default `local-dev`).

Optional tenant headers: `X-Org`, `X-Project`, `X-Environment`.

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
