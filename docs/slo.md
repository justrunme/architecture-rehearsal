# Service level objectives

**Version:** v1.5.3 · Self-hosted — operator of the deployment owns availability.

| SLO | Target |
| --- | ------ |
| API availability | 99.9% (self-hosted; operator responsibility) |
| Run accepted within | 30s of `POST /v1/runs` |
| Offline analyze (golden-size graph) | p95 under 2 minutes |
| Evidence cross-tenant leakage | **zero** |
| Gate decisions with digest chain | **100%** in production mode |
| Fail-closed on malformed YAML | **100%** without `--allow-partial` |

## Compatibility

| Component | Supported |
| --------- | --------- |
| Kubernetes | 1.30+ (manifest schemas, EndpointSlice) |
| Go toolchain | 1.26.x |
| Offline CLI | No cluster required |
| Container images | `linux/amd64`, `linux/arm64` (v1.5.3+) |
