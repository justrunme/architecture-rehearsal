# Service level objectives (v1.0)

| SLO | Target |
| --- | ------ |
| API availability | 99.9% (self-hosted; operator responsibility) |
| Run accepted within | 30s of POST /v1/runs |
| Offline analyze (golden-size graph) | p95 < 2 minutes |
| Evidence cross-tenant leakage | **zero** |
| Gate decisions with digest chain | **100%** in production mode |
| Fail-closed on malformed YAML | **100%** without `--allow-partial` |

## Compatibility

- Kubernetes: 1.30+ (manifest schemas, EndpointSlice)
- Go toolchain: 1.26.x
- Offline CLI: no cluster required
