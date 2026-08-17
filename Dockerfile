# Multi-stage build producing two images from one file:
#   docker build --target api    -> stateless HTTP/WebSocket service
#   docker build --target worker -> SQS-driven engine worker (ONNX Runtime)

FROM node:22-slim AS webbuild
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --ignore-scripts && npm rebuild esbuild
COPY web/ ./
RUN npm run build

FROM golang:1.26-bookworm AS gobuild
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=webbuild /src/web/dist ./web/dist
RUN CGO_ENABLED=1 go build -o /out/api ./cmd/api && \
    CGO_ENABLED=1 go build -o /out/worker ./cmd/worker && \
    CGO_ENABLED=0 go build -o /out/puzzleload ./cmd/puzzleload

# ONNX Runtime shared library for the worker. TARGETARCH keeps the image
# buildable on both Apple Silicon (arm64) and Fargate (amd64).
FROM debian:bookworm-slim AS ortlib
ARG TARGETARCH
ARG ORT_VERSION=1.27.1
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates wget && \
    case "${TARGETARCH}" in amd64) ORT_ARCH=x64 ;; arm64) ORT_ARCH=aarch64 ;; *) echo "unsupported arch ${TARGETARCH}" && exit 1 ;; esac && \
    wget -qO /tmp/ort.tgz "https://github.com/microsoft/onnxruntime/releases/download/v${ORT_VERSION}/onnxruntime-linux-${ORT_ARCH}-${ORT_VERSION}.tgz" && \
    mkdir -p /ort && tar -xzf /tmp/ort.tgz -C /ort --strip-components=1 && rm /tmp/ort.tgz

FROM debian:bookworm-slim AS api
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates wget && rm -rf /var/lib/apt/lists/*
COPY --from=gobuild /out/api /usr/local/bin/api
# The importer ships in the api image rather than its own, so refreshing the
# puzzle corpus on the box is one docker run against an image that is already
# there:
#   docker run --rm --entrypoint puzzleload <api image> -url <dump> -min-popularity 90
COPY --from=gobuild /out/puzzleload /usr/local/bin/puzzleload
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=3s CMD wget -qO- http://localhost:8080/healthz || exit 1
ENTRYPOINT ["api"]

FROM debian:bookworm-slim AS worker
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=ortlib /ort/lib/libonnxruntime.so* /usr/local/lib/
ENV ONNXRUNTIME_LIB=/usr/local/lib/libonnxruntime.so
# Bake the model in for environments without volume mounts (Fargate). If
# the artifact is absent the worker falls back to the material engine.
COPY models/ /models/
ENV MODEL_PATH=/models/blundernet.onnx
COPY --from=gobuild /out/worker /usr/local/bin/worker
# The worker serves no traffic, so its metrics port is the only thing that
# can answer a probe. This image has no wget, so the binary probes itself.
HEALTHCHECK --interval=15s --timeout=3s --start-period=30s \
  CMD worker -healthcheck || exit 1
ENTRYPOINT ["worker"]
