# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.27.1-bookworm AS builder

WORKDIR /src

# Keep dependency downloads in a cacheable layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/yukibot \
    ./cmd/yukibot

FROM debian:bookworm-slim

WORKDIR /app
COPY --from=builder /out/yukibot /usr/local/bin/yukibot

ENTRYPOINT ["/usr/local/bin/yukibot"]
