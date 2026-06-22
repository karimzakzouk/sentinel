# ── Builder stage: install deps ──────────────────────────────────────────
FROM python:3.12-slim AS builder

# Minimum system deps for pip builds
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl ca-certificates \
    && apt-get clean && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Common package
COPY services/common /tmp/common
RUN pip install --no-cache-dir /tmp/common

# Core deps. v2.0.0: removed kopf/aiokafka (distributed mode killed),
# removed terraform (executor frozen). Added structlog.
RUN pip install --no-cache-dir \
    fastapi uvicorn httpx tenacity structlog \
    langgraph langchain-openai openai \
    pygithub kubernetes \
    weaviate-client \
    "sqlalchemy[asyncio]" asyncpg \
    prometheus-client pydantic-settings

# ── Runtime stage ────────────────────────────────────────────────────────
FROM python:3.12-slim AS runtime

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates git \
    && apt-get clean && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /usr/local/lib/python3.12/site-packages /usr/local/lib/python3.12/site-packages
COPY --from=builder /usr/local/bin /usr/local/bin

# Copy all the service code (~600 KB total)
COPY services/common /app/services/common
COPY services/ingestion /app/services/ingestion
COPY services/detection /app/services/detection
COPY services/reasoning /app/services/reasoning
COPY services/safety /app/services/safety
COPY services/execution /app/services/execution
COPY services/feedback /app/services/feedback
COPY services/core /app/services/core

# Safety policies (mounted into OPA sidecar if needed)
COPY services/safety/policies /app/policies

ENV PYTHONUNBUFFERED=1
ENV PYTHONPATH=/app/services/common:/app/services/ingestion:/app/services/detection:/app/services/reasoning:/app/services/safety:/app/services/execution:/app/services/feedback:/app/services/core

# Run as non-root
RUN useradd -r -u 1000 -g root nonroot && chown -R nonroot:root /app
USER 1000

WORKDIR /app/services/core

EXPOSE 8000

# v2.0.0: package-style invocation. The core package has __init__.py now.
CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8000"]
