#!/usr/bin/env bash
# Read-only to production services. Requires a DISPOSABLE PostgreSQL test DB:
# repository integration tests may truncate their test tables.
set -euo pipefail
cd "$(dirname "$0")/.."
: "${YUKIBOT_DATABASE_URL:?Set YUKIBOT_DATABASE_URL to a disposable PostgreSQL test database}"
mkdir -p coverage
uv sync --frozen --dev --python 3.12
uv run python scripts/generate_casefold.py --check

# Keep unit-only and full-suite results separate. No package/file exclusions.
env -u YUKIBOT_DATABASE_URL uv run coverage run --data-file=coverage/python-unit.data -m pytest tests/unit
uv run coverage json --data-file=coverage/python-unit.data -o coverage/python-unit.json
env -u YUKIBOT_DATABASE_URL uv run coverage run --data-file=coverage/python-all.data -m pytest
uv run coverage json --data-file=coverage/python-all.data -o coverage/python-all.json
go test -count=1 -race -coverpkg=./... -coverprofile=coverage/go-unit.out ./internal/... ./cmd/...
uv run go test -count=1 -race -tags=parity -coverpkg=./... \
  -coverprofile=coverage/go-all.out ./...
uv run python scripts/coverage_summary.py coverage "$@"
