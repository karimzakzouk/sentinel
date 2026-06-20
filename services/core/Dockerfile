FROM python:3.12-slim

# Install system deps for kubernetes client + weaviate + sklearn
RUN apt-get update && apt-get install -y --no-install-recommends \
    unzip curl ca-certificates git \
    && curl -sL https://releases.hashicorp.com/terraform/1.9.5/terraform_1.9.5_linux_amd64.zip -o /tmp/tf.zip \
    && unzip /tmp/tf.zip -d /usr/local/bin \
    && rm /tmp/tf.zip \
    && apt-get clean && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy + install the common package first
COPY services/common /tmp/common
RUN pip install --no-cache-dir /tmp/common

# Install ALL service dependencies in one layer
RUN pip install --no-cache-dir \
    fastapi uvicorn httpx tenacity \
    langgraph langchain-openai openai \
    pygithub kubernetes kopf \
    weaviate-client scikit-learn numpy \
    "sqlalchemy[asyncio]" asyncpg aiokafka \
    prometheus-client pydantic-settings

# Copy all the service code
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
