# syntax=docker/dockerfile:1

FROM ghcr.io/astral-sh/uv:0.12.1 AS uv
FROM python:3.12-slim-bookworm

LABEL org.opencontainers.image.source="https://github.com/nhirsama/Yukibot" \
      org.opencontainers.image.licenses="AGPL-3.0-or-later"

COPY --from=uv /uv /uvx /bin/

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    UV_COMPILE_BYTECODE=1 \
    UV_LINK_MODE=copy \
    UV_PYTHON_DOWNLOADS=0 \
    PATH="/app/.venv/bin:$PATH"

WORKDIR /app

# 先只安装锁定的生产依赖，源码变化时可直接复用这一层缓存。
COPY pyproject.toml uv.lock ./
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --no-dev --no-install-project

COPY README.md LICENSE COPYRIGHT ./
COPY src ./src
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --no-dev --no-editable

# 使用非 root 用户运行，并只授予持久化目录写权限。
RUN useradd --create-home --uid 10001 appuser \
    && mkdir -p /app/data \
    && chown appuser:appuser /app/data

COPY --chmod=755 docker/entrypoint.py /usr/local/bin/yukibot-entrypoint

# Telegram session 和 SQLite 数据库应通过卷持久化。
VOLUME ["/app/data"]

ENTRYPOINT ["yukibot-entrypoint"]
CMD ["yukibot"]
