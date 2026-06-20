FROM python:3.12-slim AS base

WORKDIR /app
COPY services/common /tmp/common
RUN pip install --no-cache-dir /tmp/common

COPY services/feedback/pyproject.toml /app/pyproject.toml
RUN pip install --no-cache-dir fastapi uvicorn weaviate-client openai tenacity \
    "sqlalchemy[asyncio]" asyncpg

COPY services/feedback /app

ENV PYTHONUNBUFFERED=1
ENV PYTHONPATH=/app

EXPOSE 8000

CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8000"]
