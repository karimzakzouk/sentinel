FROM python:3.12-slim AS base

WORKDIR /app
COPY services/common /tmp/common
RUN pip install --no-cache-dir /tmp/common

COPY services/operator/pyproject.toml /app/pyproject.toml
RUN pip install --no-cache-dir kopf kubernetes httpx

COPY services/operator /app

ENV PYTHONUNBUFFERED=1
ENV PYTHONPATH=/app

CMD ["python", "main.py"]
