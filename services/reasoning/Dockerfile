FROM python:3.12-slim AS base

WORKDIR /app
COPY services/common /tmp/common
RUN pip install --no-cache-dir /tmp/common

COPY services/reasoning/pyproject.toml /app/pyproject.toml
RUN pip install --no-cache-dir fastapi uvicorn langgraph langchain-openai openai \
    tenacity httpx weaviate-client pygithub "sqlalchemy[asyncio]" asyncpg

COPY services/reasoning /app

ENV PYTHONUNBUFFERED=1
ENV PYTHONPATH=/app

EXPOSE 8000

CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8000"]
