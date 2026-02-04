# kube-restarter

A WatchTower-style pod updater for Kubernetes. Watches for Deployments using `latest` tags and automatically restarts pods when a newer image is available in the registry.

## How It Works

kube-restarter runs as a Deployment inside your cluster and polls on a configurable interval (default 6 hours). Each cycle it:

1. Lists all Deployments annotated with `kube-restarter.io/enabled: "true"`
2. For each matching Deployment, finds containers using the `latest` tag with `imagePullPolicy: Always`
3. Queries the container registry for the current digest of the `latest` tag via the OCI Distribution API (`HEAD /v2/<repo>/manifests/<tag>`)
4. Compares the remote digest against the running pod's `imageID`
5. Deletes pods with stale images — the Deployment controller recreates them, pulling the newer image

Registry auth is handled via `imagePullSecrets` on the pod spec. Public images use anonymous access. Supports Docker Hub, GHCR, and any OCI-compliant registry.

## Configuration

| Environment Variable | Default | Description |
|---|---|---|
| `CHECK_INTERVAL` | `21600` | Seconds between reconciliation loops |
| `NAMESPACE` | `""` (all) | Restrict to a specific namespace |

## Installation

### Helm

```bash
helm install kube-restarter ./chart/kube-restarter -n kube-system --create-namespace
```

Override defaults:

```bash
helm install kube-restarter ./chart/kube-restarter -n kube-system --create-namespace \
  --set image.repository=myregistry/kube-restarter \
  --set checkInterval=120
```

### Plain Manifests

```bash
kubectl apply -f deploy/rbac.yaml
kubectl apply -f deploy/deployment.yaml
```

## Usage

Annotate any Deployment whose pods you want auto-restarted:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-app
  annotations:
    kube-restarter.io/enabled: "true"
spec:
  template:
    spec:
      containers:
        - name: my-app
          image: nginx:latest
          imagePullPolicy: Always
```

An example is provided in `deploy/example.yaml`.

## Building

```bash
docker build -t kube-restarter:latest .
```

## Dan's Note

This application is shamelessly vibe-coded. This should not really be used for production anything in kubernetes, as I built this mostly for homelab use since there doesn't exist anything similar to WatchTower's "hands-off automation" method for keeping images up to date.

### TODO
- Automated builds deployed to ghcr
- Proper logging
