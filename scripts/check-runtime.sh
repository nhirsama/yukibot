#!/usr/bin/env bash
# Use only a disposable PostgreSQL database: integration tests clean test tables.
set -euo pipefail
cd "$(dirname "$0")/.."
: "${YUKIBOT_DATABASE_URL:?Set YUKIBOT_DATABASE_URL to a disposable PostgreSQL test database}"
mkdir -p coverage
go vet ./...
go test -count=1 -timeout=2m -race -coverpkg=./... -coverprofile=coverage/go.out ./...
go tool cover -func=coverage/go.out
go build -o coverage/yukibot ./cmd/yukibot
