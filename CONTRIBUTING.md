# Contributing to Sentinel

Thanks for your interest in contributing! This guide covers the basics.

## Development setup

```bash
git clone https://github.com/karimzakzoukz/sentinel.git
cd sentinel
pip install -e services/common
pip install pytest pytest-asyncio langgraph langchain-openai openai \
  pygithub kubernetes weaviate-client scikit-learn httpx tenacity structlog \
  "sqlalchemy[asyncio]" asyncpg fastapi uvicorn prometheus-client ruff mypy pre-commit
pre-commit install
```

## Code structure

Read [README.md](README.md) for the high-level architecture. The key directories:

- `services/common/` — shared Pydantic schemas, config, logging. **Strict mypy**.
- `services/core/` — the single-process entrypoint. Split into `loops/`, `k8s/`, `app.py`, `runtime_settings.py`.
- `services/detection/` — anomaly detectors (rules, CUSUM, IsolationForest).
- `services/reasoning/` — LangGraph multi-agent graph + LLM router + context fetcher.
- `services/safety/` — OPA Rego policies.
- `services/execution/` — GitHub/K8s executors, sandbox, Kaniko builder.
- `services/feedback/` — Beta tracker, Weaviate client, postmortem.
- `chart/` — Helm chart.
- `ui/` — Next.js dashboard.
- `tests/` — pytest unit tests + `tests/integration/` for kind-cluster tests.

## Coding standards

- **Python 3.11+** — we use modern syntax (`X | None`, `match` statements, etc.).
- **Pydantic v2** for all data models. Use `model_dump()` not `dict()`.
- **Async everywhere** — every I/O function is `async def`. Use `asyncio.to_thread()` for sync libs (PyGithub, K8s client).
- **Typed** — all public functions have type hints. `services/common/` is `mypy --strict`.
- **Structured logging** — use `from sentinel_common.logging import get_logger` and bind context with `bind_context(incident_id=..., service=...)`.
- **No `print()`** — use the logger.
- **No bare `except:`** — catch specific exceptions or use `except Exception as e:` with a comment.

## How to add a new detector

1. Create `services/detection/detectors/your_detector.py`:
   ```python
   from sentinel_common import Anomaly, Signal

   class YourDetector:
       async def observe(self, sig: Signal) -> list[Anomaly]:
           # ... your logic
           return []
   ```
2. Wire it into `services/core/main.py` lifespan + `services/core/loops/detection.py`.
3. Add tests in `tests/test_detection.py`.
4. Run `ruff check services/detection/detectors/your_detector.py` and `mypy services/detection/detectors/your_detector.py`.

## How to add a new executor

1. Create `services/execution/executors/your_executor.py` with a class that has `async def execute(self, incident, result) -> ExecutionResult`.
2. Add a `FixType` enum value in `services/common/sentinel_common/schemas.py` if needed.
3. Wire it into `services/core/loops/execution.py`'s `_execute_incident` switch.
4. Add tests in `tests/test_your_executor.py`.

## How to add an OPA policy

1. Edit `services/safety/policies/sentinel.rego` (the source of truth).
2. Mirror the change in `chart/templates/opa/policies-configmap.yaml` (kept in sync — see comment in that file).
3. Add a test in `tests/test_safety.py` (TBD — currently no safety policy tests, this is a v2.1 goal).
4. Run `helm lint chart/` to make sure the chart still renders.

## Testing

- **Unit tests**: `pytest tests/` — runs in seconds, no cluster needed.
- **Integration tests**: `pytest -m integration` — requires a kind cluster + Docker Hub creds + LLM key. Set `SENTINEL_RUN_INTEGRATION=1` to enable.
- **Helm tests**: `pytest tests/test_helm_chart.py` — runs `helm lint` and `helm template` to verify the chart renders.

## Pre-commit hooks

We use ruff (lint + format), mypy (strict on `services/common/`), and standard hygiene checks (trailing whitespace, large files, merge conflict markers, private keys).

```bash
pre-commit install   # once
pre-commit run --all-files  # manually
```

## Commit messages

Follow [Conventional Commits](https://www.conventionalcommits.org/):

- `feat: add OOM detector`
- `fix: prevent CRD watcher from re-triggering on status updates`
- `refactor: split core/main.py into loops/`
- `docs: add MIGRATION.md`
- `test: add k8s_helpers tests`
- `chore: bump ruff to 0.6.9`

## Pull requests

- One logical change per PR.
- Include tests for new behavior.
- Update `MIGRATION.md` if the change is breaking.
- Don't bump the chart version in a PR — we do that at release time.
