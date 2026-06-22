# ── Builder stage: install deps ──────────────────────────────────────────
FROM python:3.12-slim AS builder

# Install only the minimum system deps needed for pip builds
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl ca-certificates \
    && apt-get clean && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy + install the common package first
COPY services/common /tmp/common
RUN pip install --no-cache-dir /tmp/common

# Install ONLY the deps core mode actually needs.
# Removed (moved to their respective service images or not needed in core mode):
#   - scikit-learn + scipy + numpy → only used by IsolationForestDetector
#     (detection image). Core mode uses rules-based detector only.
#   - kopf + aiokafka → only for distributed mode (Kafka). Core mode uses
#     in-memory bus.
#   - Terraform binary → only for infrastructure fix type. Not used in core mode.
RUN pip install --no-cache-dir \
    fastapi uvicorn httpx tenacity \
    langgraph langchain-openai openai \
    pygithub kubernetes \
    weaviate-client \
    "sqlalchemy[asyncio]" asyncpg \
    prometheus-client pydantic-settings

# ── Runtime stage: minimal image, no build tools ────────────────────────
FROM python:3.12-slim AS runtime

# Only install runtime deps (no build tools like gcc)
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && apt-get clean && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy installed Python packages from builder
COPY --from=builder /usr/local/lib/python3.12/site-packages /usr/local/lib/python3.12/site-packages
COPY --from=builder /usr/local/bin /usr/local/bin

# Copy all the service code (only ~500 KB total)
COPY services/common /app/services/common
COPY services/ingestion /app/services/ingestion
COPY services/detection /app/services/detection
COPY services/reasoning /app/services/reasoning
COPY services/safety /app/services/safety
COPY services/execution /app/services/execution
COPY services/feedback /app/services/feedback
COPY services/core /app/services/core

# Also copy the safety policies so they can be mounted into OPA if needed
COPY services/safety/policies /app/policies

ENV PYTHONUNBUFFERED=1
ENV PYTHONPATH=/app/services/common:/app/services/ingestion:/app/services/detection:/app/services/reasoning:/app/services/safety:/app/services/execution:/app/services/feedback:/app/services/core

WORKDIR /app/services/core

EXPOSE 8000

CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8000"]
