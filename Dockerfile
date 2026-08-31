FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/nvr-evidence ./cmd/server

FROM alpine:3.22
RUN apk add --no-cache ca-certificates ffmpeg tzdata && addgroup -S app && adduser -S -G app app
LABEL org.opencontainers.image.source="https://github.com/RickDB/HikFootageServer" \
      org.opencontainers.image.url="https://github.com/RickDB/HikFootageServer" \
      org.opencontainers.image.title="HikFootageServer" \
      org.opencontainers.image.description="Web-based Hikvision NVR evidence retrieval over ISAPI"
WORKDIR /app
COPY --from=build /out/nvr-evidence /usr/local/bin/nvr-evidence
RUN mkdir -p /app/data /app/exports && chown -R app:app /app
USER app
EXPOSE 8080
ENV APP_LISTEN_ADDR=:8080 APP_DATABASE_PATH=/app/data/hikvision.db APP_EXPORT_ROOT=/app/exports APP_FFMPEG_PATH=ffmpeg
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1
ENTRYPOINT ["nvr-evidence"]
