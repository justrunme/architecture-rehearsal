# Control plane API (v1.2)

Base URL: `http://localhost:8080`

Auth (v1.0.1+): `Authorization: Bearer <token>`.

- Set `REHEARSAL_API_TOKEN` (required to start `serve` in production).
- Optional multi-token map: `REHEARSAL_API_TOKENS` JSON.
- Local only: `rehearsal serve --insecure-dev` enables `local-dev`.
- **Hardcoded `ci` / `viewer-token` removed.**
- Client `X-Org` does **not** change principal org; tenant is bound to the token.
- Object reads use labels of the stored object (cross-tenant → 404).

## Serve flags (v1.2)

| Flag | Default | Meaning |
| ---- | ------- | ------- |
| `--db` | `<workdir>/rehearsal.db` or `data/rehearsal.db` | SQLite path or `postgres://` URL |
| `--blob` | `<workdir>/blobs` or `data/blobs` | Content-addressed blob root |
| `--memory` | off | Non-durable in-process store (tests/dev) |
| `--async` | off | Enqueue advances as durable jobs; start workers |
| `--workers` | 1 | Worker count when `--async` |
| `--workdir` | empty | Sandbox root for file refs |
| `--insecure-dev` | off | Allow `local-dev` token only |

Env: `REHEARSAL_DATABASE_URL`, `REHEARSAL_BLOB_ROOT`.

## Endpoints

| Method | Path | Action |
| ------ | ---- | ------ |
| GET | /healthz | Liveness |
| GET | /readyz | Readiness (`async` flag) |
| GET | /v1/version | Version (`1.2.0`) |
| GET | /v1/metrics | Prometheus text (requests, jobs, uptime) |
| GET | /v1/schemas | Contract catalog |
| POST | /v1/runs | Create run (idempotencyKey supported) |
| GET | /v1/runs | List runs (org-scoped) |
| GET | /v1/runs/{id} | Get run |
| POST | /v1/runs/{id}/advance | Sync execute, cancel, or async enqueue |
| GET | /v1/runs/{id}/evidence | Digests + chain/DSSE when present |
| POST | /v1/clusters | Register cluster (secret ref only) |
| GET | /v1/clusters | List clusters |
| POST | /v1/policies | Store policy document (bound on run create) |
| GET | /v1/policies | List policies |
| GET | /v1/calibration | Scenario quality report |

### Advance semantics

```json
POST /v1/runs/{id}/advance
{ "workDir": ".", "action": "", "async": true }
```

- Sync (default unless server `--async`): executes engine, returns run JSON **200**.
- Async: returns **202** `{ "runId", "jobId", "status": "queued" }`.
- `action: "cancel"` always sync-cancels.

All mutations should send `idempotencyKey` for safe retries.
