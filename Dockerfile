# syntax=docker/dockerfile:1
# jamypg NL2SQL MCP server (postgres/mysql/mariadb text2sql) — self-contained image for air-gapped deployment.
# Metadata (data/metadb) is baked in; mount a volume over /app/data/metadb to
# override, and mount /app/data/metadb/feedback + audit for persistence.

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/jamypg-mcp ./cmd/jamypg-mcp \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/jamypg-eval ./cmd/jamypg-eval \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/jamypg-goldgen ./cmd/jamypg-goldgen

FROM alpine:3.21
RUN adduser -D -u 10001 jamypg
COPY --from=build /out/jamypg-mcp /out/jamypg-eval /out/jamypg-goldgen /usr/local/bin/
COPY --chown=jamypg:jamypg data/metadb /app/data/metadb
WORKDIR /app
USER jamypg
EXPOSE 9797
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s \
  CMD wget -qO- http://127.0.0.1:9797/healthz >/dev/null 2>&1 || exit 1
ENTRYPOINT ["jamypg-mcp"]
CMD ["-transport", "http", "-addr", "0.0.0.0:9797", "-data", "/app/data/metadb"]
