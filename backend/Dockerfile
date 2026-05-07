# syntax=docker/dockerfile:1.7

FROM golang:1.22-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags "-s -w" -o /out/restream-api ./cmd/api

FROM alpine:3.20
RUN apk add --no-cache ffmpeg ca-certificates tzdata curl \
 && adduser -D -u 1000 app \
 && mkdir -p /data/cache \
 && chown -R app:app /data
USER app
WORKDIR /app
COPY --from=build /out/restream-api /app/restream-api
ENV HTTP_ADDR=:2087 \
    CACHE_DIR=/data/cache
EXPOSE 2087
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -fsS http://127.0.0.1:2087/health || exit 1
ENTRYPOINT ["/app/restream-api"]
