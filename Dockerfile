FROM golang:1.26-bookworm AS builder

WORKDIR /src

COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/minime-server ./cmd/minime-server

FROM python:3.11-slim-bookworm

WORKDIR /app

ENV PYTHONUNBUFFERED=1 \
    PORT=10000 \
    MINIME_PORT=10000 \
    MINIME_DATA_ROOT=/data \
    MINIME_GENERATOR_MODE=script \
    MINIME_RUN_WORKERS=true \
    MINIME_WORKER_COUNT=1 \
    MINIME_WORKER_POLL_INTERVAL_MS=250 \
    UV_SYSTEM_PYTHON=1

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    ffmpeg \
    && rm -rf /var/lib/apt/lists/*

RUN pip install --no-cache-dir uv rembg pillow

COPY --from=builder /out/minime-server /app/bin/minime-server
COPY . /app

EXPOSE 8080

CMD ["/app/bin/minime-server"]
