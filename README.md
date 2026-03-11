# resource-indexer

`resource-text-indexer` keeps an in-memory, current-state index of namespaced Kubernetes resources and serves it over HTTP.
It bootstraps watchers once at startup and watches `CustomResourceDefinition` objects to start new CRD watchers near real time.

## Install ready image (quick start)

Use the prebuilt image from this repo (`ghcr.io/flosch62/resource-indexer:v0.2.0`):

```bash
kubectl apply -k "https://github.com/FloSch62/resource-indexer//packages/resource-text-indexer?ref=main"
```

## What it serves

- `GET /resources.txt`: plain-text lines in format `<namespace>/<apiVersion>/<kind>/<name>`
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

## Runtime config

Environment variables:

- `PORT` (default `8080`)
- `MAX_PAGE_SIZE` (default `5000`, range `100..100000`)
- `RESYNC_PERIOD` (default `10m`)
- `EXCLUDE_GVRS` (comma-separated `group/version/resource`, core API as `v1/resource`)
- `EXCLUDE_NAMESPACES` (comma-separated namespace names)
