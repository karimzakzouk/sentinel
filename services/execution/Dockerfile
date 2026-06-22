FROM python:3.12-slim AS base

# Install terraform CLI for the terraform executor
RUN apt-get update && apt-get install -y --no-install-recommends \
    unzip curl ca-certificates git \
    && curl -sL https://releases.hashicorp.com/terraform/1.9.5/terraform_1.9.5_linux_amd64.zip -o /tmp/tf.zip \
    && unzip /tmp/tf.zip -d /usr/local/bin \
    && rm /tmp/tf.zip \
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

EXPOSE 8000

CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8000"]
