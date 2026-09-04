# syntax=docker/dockerfile:1

FROM golang:1.23-alpine AS builder

WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/vertical-intel-video-agent ./cmd/server

FROM alpine:3.20

RUN addgroup -S app && adduser -S -G app app \
    && mkdir -p /app/data \
    && chown -R app:app /app

WORKDIR /app
COPY --from=builder --chown=app:app /out/vertical-intel-video-agent /app/vertical-intel-video-agent

ENV ADDR=0.0.0.0:8088 \
    VIDEO_FACTORY_DATA_DIR=/app/data

USER app
EXPOSE 8088
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget -q -O - http://127.0.0.1:8088/health >/dev/null || exit 1

ENTRYPOINT ["/app/vertical-intel-video-agent"]
