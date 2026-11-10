FROM python:3.13-slim
WORKDIR /app
COPY catalog.py app.py ./
USER 65532:65532
CMD ["python", "-u", "app.py"]
