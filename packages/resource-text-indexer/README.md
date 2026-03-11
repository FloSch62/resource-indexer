# resource-text-indexer (kpt package)

Real-time current-state index for all namespaced Kubernetes resources.

## Endpoints

- `GET /resources.txt`: one plain-text line per current resource in format `<namespace>/<apiVersion>/<kind>/<name>`
- `GET /snapshot.json`: JSON array of the same line strings
- `GET /`: same as `/resources.txt`, served with `text/html` content type (machine-oriented text)
- `GET /healthz`, `GET /readyz`

## Install with kpt

```bash
kpt pkg get <YOUR_GIT_REPO>/packages/resource-text-indexer@<REF>
kpt live init resource-text-indexer
kpt live apply resource-text-indexer --reconcile-timeout=2m --output=table
```

## Install with kubectl (no clone)

```bash
kubectl apply -k "https://github.com/FloSch62/resource-indexer//packages/resource-text-indexer?ref=main"
```

## Container image

Use a persistent image registry (for example GHCR, ECR, GCR, or ACR), not `ttl.sh`.

```bash
export IMAGE=ghcr.io/flosch62/resource-indexer:latest
docker build -t "$IMAGE" .
docker push "$IMAGE"
kubectl -n resource-text-indexer set image deployment/resource-text-indexer app="$IMAGE"
```

## Config

Environment variables in `deployment.yaml`:

- `PORT` (default `8080`)
- `MAX_PAGE_SIZE` (default `5000`)
- `RESYNC_PERIOD` (default `10m`)
- `DISCOVERY_INTERVAL` (default `60s`)
- `EXCLUDE_GVRS` (comma-separated, format: `group/version/resource`; core API: `v1/resource`)
- `EXCLUDE_NAMESPACES` (comma-separated namespace names)

## Notes

- Requires cluster-wide read permissions to list/watch resources.
- Includes `Secret` metadata only (never values).
- Output is current full snapshot only; no history or event stream fields.
