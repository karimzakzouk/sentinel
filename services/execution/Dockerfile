FROM python:3.12-slim AS base

# Runtime deps for git operations (Kaniko clone-and-patch path uses git)
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates git \
    && apt-get clean && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY services/common /tmp/common
RUN pip install --no-cache-dir /tmp/common

COPY services/execution/pyproject.toml /app/pyproject.toml
RUN pip install --no-cache-dir fastapi uvicorn pygithub kubernetes httpx \
    "sqlalchemy[asyncio]" asyncpg

COPY services/execution /app

ENV PYTHONUNBUFFERED=1
ENV PYTHONPATH=/app

# Run as non-root
RUN useradd -r -u 1000 -g root nonroot && chown -R nonroot:root /app
USER 1000

EXPOSE 8000
