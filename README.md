# resource-indexer

`resource-text-indexer` keeps an in-memory, current-state index of namespaced Kubernetes resources and serves it over HTTP.
It bootstraps watchers once at startup and watches `CustomResourceDefinition` objects to start new CRD watchers near real time.

## Endpoints

- `GET /resources.txt`: one plain-text line per current resource in format `<namespace>/<apiVersion>/<kind>/<name> labels=<json-object>`
- `GET /snapshot.json`: JSON array of the same line strings
- `GET /`: same output as `/resources.txt`
- `GET /healthz`
- `GET /readyz`

Query params for `/resources.txt`, `/`, and `/snapshot.json`:

- `namespace`
- `group`
- `version`
- `kind` (case-insensitive)
- `limit` (capped by `MAX_PAGE_SIZE`)
- `offset`

## Install

### Install with kubectl

Use the prebuilt image from this repo (`ghcr.io/flosch62/resource-indexer:v0.3.0`):

```bash
kubectl apply -k "https://github.com/FloSch62/resource-indexer//packages/resource-text-indexer?ref=main"
```

### Install with kpt

```bash
kpt pkg get https://github.com/FloSch62/resource-indexer.git/packages/resource-text-indexer@main resource-text-indexer
kpt live init resource-text-indexer
kpt live apply resource-text-indexer --reconcile-timeout=2m --output=table
```

## Uninstall

### Uninstall with kubectl

```bash
kubectl delete -k "https://github.com/FloSch62/resource-indexer//packages/resource-text-indexer?ref=main"
```

### Uninstall with kpt

```bash
kpt live destroy resource-text-indexer --output=table
```

## Container image

Use a persistent image registry (for example GHCR, ECR, GCR, or ACR), not `ttl.sh`.

```bash
export IMAGE=ghcr.io/flosch62/resource-indexer:v0.3.0
docker build -t "$IMAGE" .
docker push "$IMAGE"
kubectl -n resource-text-indexer set image deployment/resource-text-indexer app="$IMAGE"
```

## Runtime config

Environment variables:

- `PORT` (default `8080`)
- `MAX_PAGE_SIZE` (default `5000`, range `100..100000`)
- `RESYNC_PERIOD` (default `10m`)
- `EXCLUDE_GVRS` (comma-separated `group/version/resource`, core API as `v1/resource`)
- `EXCLUDE_NAMESPACES` (comma-separated namespace names)

## Notes

- Requires cluster-wide read permissions to list/watch resources.
- Includes `Secret` metadata only (never values).