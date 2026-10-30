FROM golang:1.26.1-alpine3.23 AS build

WORKDIR /src
COPY api_host/src/go.mod api_host/src/go.sum ./
#RUN apk add --no-cache git
RUN go mod download
COPY api_host/src/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api-host .
RUN mkdir -p /api-host-state && touch /api-host-state/.keep

FROM curlimages/curl:8.14.1@sha256:9a1ed35addb45476afa911696297f8e115993df459278ed036182dd2cd22b67b
COPY --from=build /out/api-host /api-host
COPY --from=build --chown=65532:65532 /api-host-state/.keep /var/lib/api-host/.keep
USER 65532:65532
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
	CMD ["/api-host", "healthcheck"]
ENTRYPOINT ["/api-host"]