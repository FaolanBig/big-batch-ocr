FROM golang:1.26.1-alpine3.23 AS build

WORKDIR /src
COPY api_host/src/go.mod api_host/src/go.sum ./
RUN apt-get update && \
    apt-get install -y git && \
    rm -rf /var/lib/apt/lists/*
RUN GOPROXY=direct go mod download
COPY api_host/src/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api-host .
RUN mkdir -p /api-host-state && touch /api-host-state/.keep

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/api-host /api-host
COPY --from=build --chown=65532:65532 /api-host-state/.keep /var/lib/api-host/.keep
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
	CMD ["/api-host", "healthcheck"]
ENTRYPOINT ["/api-host"]
