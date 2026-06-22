FROM python:3.12-slim AS base

# Install the shared common package first so it's cached across service builds
WORKDIR /app
COPY services/common /tmp/common
RUN pip install --no-cache-dir /tmp/common

# Install this service's deps
COPY services/ingestion/pyproject.toml /app/pyproject.toml
RUN pip install --no-cache-dir fastapi uvicorn httpx kubernetes

# Copy the service code
COPY services/ingestion /app

ENV PYTHONUNBUFFERED=1
ENV PYTHONPATH=/app

EXPOSE 8000

CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8000"]
