# Architecture Rehearsal Helm Chart

Deterministic pre-deploy failure simulation and post-deploy verification control plane.

## Overview

Architecture Rehearsal helps platform teams know what breaks before deploying infrastructure changes. It builds a causal architecture graph from cluster baselines, rehearses proposed changes, and gates deployments with deterministic policy decisions.

## Features

- **Control plane API**: HTTP API with token authentication for orchestrating rehearsals
- **Kubernetes operator**: Controller-runtime based operator for managing RehearsalRun CRs
- **Argo CD PreSync gate**: Block risky GitOps syncs before they reach the cluster
- **Evidence chain**: Content-addressed verification with cryptographic signatures
- **Durable storage**: SQLite (default) or PostgreSQL backend with optional S3-compatible object storage

## Quick Start

```bash
helm upgrade --install rehearsal oci://ghcr.io/justrunme/charts/architecture-rehearsal \
  --version 1.8.4 \
  --set api.token="$(openssl rand -hex 32)"
```

## Installation

### Basic installation (control plane only)

```bash
helm upgrade --install rehearsal oci://ghcr.io/justrunme/charts/architecture-rehearsal \
  --version 1.8.4 \
  --set api.token="$(openssl rand -hex 32)" \
  --set persistence.enabled=true \
  --set persistence.size=10Gi
```

### With Kubernetes operator

```bash
helm upgrade --install rehearsal oci://ghcr.io/justrunme/charts/architecture-rehearsal \
  --version 1.8.4 \
  --set api.token="$(openssl rand -hex 32)" \
  --set operator.enabled=true
```

The CRD is automatically installed from the chart's `crds/` directory during `helm install`.

### With Argo CD PreSync gate

```bash
helm upgrade --install rehearsal oci://ghcr.io/justrunme/charts/architecture-rehearsal \
  --version 1.8.4 \
  --set api.token="$(openssl rand -hex 32)" \
  --set argoPresync.enabled=true \
  --set argoPresync.appName="my-application" \
  --set argoPresync.appNamespace="argocd"
```

## Configuration

### API Configuration

The API token is **required**. Set it via:

```bash
# Generate a secure token
helm upgrade --install rehearsal ... \
  --set api.token="$(openssl rand -hex 32)"

# Or use an existing secret
helm upgrade --install rehearsal ... \
  --set api.existingSecret="rehearsal-token" \
  --set api.existingSecretKey="token"
```

### Persistence

By default, the chart creates a 5Gi PVC for SQLite and content-addressed blobs:

```yaml
persistence:
  enabled: true
  size: 5Gi
  accessMode: ReadWriteOnce
  storageClass: ""  # Uses cluster default
```

For production, consider using PostgreSQL:

```yaml
postgresql:
  enabled: true
  host: "postgres.database.svc.cluster.local"
  database: "rehearsal"
```

Set `REHEARSAL_DATABASE_URL` via an existing secret for connection credentials.

### Operator

The operator manages RehearsalRun custom resources:

```yaml
operator:
  enabled: true
  replicas: 2
  leaderElection: true
  image:
    repository: ghcr.io/justrunme/architecture-rehearsal-operator
    tag: "1.8.4"
```

The operator communicates with the control plane API using the same token configured for the API.

**Security note**: The operator Deployment contains the control plane URL and token. The RehearsalRun CR never exposes these fields (removed in v1.5.1).

### Argo CD PreSync Gate

Enable the PreSync gate to block risky syncs before they reach the cluster:

```yaml
argoPresync:
  enabled: true
  includeHookJob: false  # Set true to include the Job template
  clusterRBAC: false     # Set true for Applications/namespaces/PVs access
  desiredConfigMap: "rehearsal-argo-presync-desired"
  appName: ""            # Required for Job template
  appNamespace: "argocd"
  timeout: 90s
```

See [docs/argo-presync-gate.md](https://github.com/justrunme/architecture-rehearsal/blob/main/docs/argo-presync-gate.md) for details.

## Values Reference

| Parameter | Description | Default |
|-----------|-------------|---------|
| `replicaCount` | Number of control plane replicas | `1` |
| `image.repository` | Control plane image repository | `ghcr.io/justrunme/architecture-rehearsal` |
| `image.tag` | Control plane image tag | `1.8.4` |
| `image.pullPolicy` | Image pull policy | `IfNotPresent` |
| `service.type` | Service type | `ClusterIP` |
| `service.port` | Service port | `8080` |
| `api.enabled` | Enable API server | `true` |
| `api.token` | API authentication token (required) | `""` |
| `api.existingSecret` | Use existing secret for token | `""` |
| `api.org` | Default organization | `default` |
| `api.role` | Default role | `platform-admin` |
| `api.async` | Enable async job processing | `true` |
| `api.workers` | Number of async workers | `2` |
| `persistence.enabled` | Enable persistent storage | `true` |
| `persistence.size` | PVC size | `5Gi` |
| `persistence.accessMode` | PVC access mode | `ReadWriteOnce` |
| `persistence.storageClass` | Storage class | `""` |
| `persistence.existingClaim` | Use existing PVC | `""` |
| `postgresql.enabled` | Enable PostgreSQL backend | `false` |
| `postgresql.host` | PostgreSQL host | `""` |
| `postgresql.database` | PostgreSQL database | `rehearsal` |
| `objectStorage.enabled` | Enable S3-compatible object storage | `false` |
| `objectStorage.endpoint` | Object storage endpoint | `""` |
| `objectStorage.bucket` | Object storage bucket | `rehearsal-evidence` |
| `operator.enabled` | Enable Kubernetes operator | `false` |
| `operator.replicas` | Number of operator replicas | `2` |
| `operator.leaderElection` | Enable leader election | `true` |
| `operator.image.repository` | Operator image repository | `ghcr.io/justrunme/architecture-rehearsal-operator` |
| `operator.image.tag` | Operator image tag | `1.8.4` |
| `operator.controlPlaneURL` | Override control plane URL | `""` |
| `operator.existingSecret` | Use existing secret for token | `""` |
| `operator.networkPolicy.enabled` | Enable NetworkPolicy | `false` |
| `operator.networkPolicy.kubeAPIServerCIDRs` | Kubernetes API CIDRs | `[]` |
| `argoPresync.enabled` | Enable PreSync RBAC | `false` |
| `argoPresync.includeHookJob` | Include PreSync Job template | `false` |
| `argoPresync.clusterRBAC` | Enable cluster-level RBAC | `false` |
| `argoPresync.desiredConfigMap` | ConfigMap name for desired state | `rehearsal-argo-presync-desired` |
| `argoPresync.appName` | Argo CD Application name | `""` |
| `argoPresync.appNamespace` | Argo CD namespace | `argocd` |
| `argoPresync.timeout` | PreSync timeout | `90s` |
| `argoPresync.image.repository` | PreSync image repository | `ghcr.io/justrunme/architecture-rehearsal-presync` |
| `argoPresync.image.tag` | PreSync image tag | `1.8.4` |
| `resources.requests.cpu` | CPU request | `50m` |
| `resources.requests.memory` | Memory request | `64Mi` |
| `resources.limits.cpu` | CPU limit | `500m` |
| `resources.limits.memory` | Memory limit | `256Mi` |
| `serviceAccount.create` | Create service account | `true` |
| `serviceAccount.name` | Service account name | `""` |
| `rbac.create` | Create RBAC resources | `true` |

## Images

All images are multi-arch (`linux/amd64` + `linux/arm64`):

- Control plane: `ghcr.io/justrunme/architecture-rehearsal:1.8.4`
- Operator: `ghcr.io/justrunme/architecture-rehearsal-operator:1.8.4`
- PreSync gate: `ghcr.io/justrunme/architecture-rehearsal-presync:1.8.4`

Images are signed with Cosign and include SBOM attestations.

### Verifying the signed chart

The Helm chart OCI artifact is signed with Cosign (keyless). Verify it before installation:

```bash
# Install cosign if not already available
# https://docs.sigstore.dev/cosign/installation/

# Verify the chart signature
cosign verify \
  --certificate-identity-regexp="^https://github.com/justrunme/architecture-rehearsal/" \
  --certificate-oidc-issuer=https://token.actions.githubusercontent.com \
  oci://ghcr.io/justrunme/charts/architecture-rehearsal:1.8.4
```

## Documentation

- [Project README](https://github.com/justrunme/architecture-rehearsal)
- [Operator Documentation](https://github.com/justrunme/architecture-rehearsal/blob/main/docs/operator.md)
- [Operator Security](https://github.com/justrunme/architecture-rehearsal/blob/main/docs/operator-security.md)
- [API Documentation](https://github.com/justrunme/architecture-rehearsal/blob/main/docs/api.md)
- [Argo CD PreSync Gate](https://github.com/justrunme/architecture-rehearsal/blob/main/docs/argo-presync-gate.md)
- [Evidence Chain](https://github.com/justrunme/architecture-rehearsal/blob/main/docs/EVIDENCE.md)

## Support

- GitHub Issues: https://github.com/justrunme/architecture-rehearsal/issues
- Case Study: https://justrunme.com/cases/architecture-rehearsal/

## License

Apache-2.0
