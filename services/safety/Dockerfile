FROM python:3.12-slim AS base

WORKDIR /app
COPY services/common /tmp/common
RUN pip install --no-cache-dir /tmp/common

COPY services/safety/pyproject.toml /app/pyproject.toml
RUN pip install --no-cache-dir fastapi uvicorn httpx "sqlalchemy[asyncio]" asyncpg

COPY services/safety /app

# Copy the Rego policies so they can be mounted into the OPA sidecar via a
# configmap (the chart's templates/opa.yaml does this).
COPY services/safety/policies /app/policies

ENV PYTHONUNBUFFERED=1
ENV PYTHONPATH=/app

EXPOSE 8000

CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8000"]
