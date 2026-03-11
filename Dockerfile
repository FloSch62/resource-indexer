FROM golang:1.24.2 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /out/resource-text-indexer ./cmd/resource-text-indexer

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /out/resource-text-indexer /resource-text-indexer
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/resource-text-indexer"]
