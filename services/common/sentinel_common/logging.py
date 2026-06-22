"""Structured logging setup using structlog.

v2.0.0: replaces stdlib logging with structlog for JSON output in production
and pretty console output in dev. Every log line automatically carries
incident_id, service, namespace, agent context via context vars.

Usage:
    from sentinel_common.logging import configure_logging, get_logger
    configure_logging()  # call once at startup
    log = get_logger("sentinel.core")
    log.info("starting", version="2.0.0")
    # → {"event": "starting", "version": "2.0.0", "logger": "sentinel.core", ...}

To bind context (e.g. incident_id) for all logs in a coroutine:
    from sentinel_common.logging import bind_context, clear_context
    bind_context(incident_id=str(inc_id), service="api", namespace="prod")
    log.info("investigation_started")  # includes incident_id, service, namespace
    clear_context()
"""

from __future__ import annotations

import logging
import os
import sys
from typing import Any

try:
    import structlog

    HAS_STRUCTLOG = True
except ImportError:
    HAS_STRUCTLOG = False
    structlog = None

_CONFIGURED = False


def configure_logging(*, json_output: bool | None = None, level: str = "INFO") -> None:
    """Configure structlog. Safe to call multiple times (no-op after first).

    Args:
        json_output: Force JSON (True) or pretty console (False). If None,
            auto-detected from env: SENTINEL_LOG_JSON=1 → JSON, else pretty.
        level: Log level name (DEBUG, INFO, WARNING, ERROR).
    """
    global _CONFIGURED
    if _CONFIGURED:
        return

    if not HAS_STRUCTLOG:
        # Fallback to stdlib logging if structlog isn't installed
        logging.basicConfig(
            level=getattr(logging, level.upper(), logging.INFO),
            format="%(asctime)s %(levelname)s %(name)s: %(message)s",
        )
        _CONFIGURED = True
        return

    if json_output is None:
        json_output = os.getenv("SENTINEL_LOG_JSON", "").lower() in ("1", "true", "yes")

    # Shared processors for both stdlib and structlog
    shared_processors = [
        structlog.contextvars.merge_contextvars,
        structlog.stdlib.add_logger_name,
        structlog.stdlib.add_log_level,
        structlog.processors.TimeStamper(fmt="iso", utc=True),
        structlog.processors.StackInfoRenderer(),
        structlog.processors.format_exc_info,
    ]

    structlog.configure(
        processors=shared_processors
        + [
            structlog.stdlib.ProcessorFormatter.wrap_for_formatter,
        ],
        logger_factory=structlog.stdlib.LoggerFactory(),
        wrapper_class=structlog.stdlib.BoundLogger,
        cache_logger_on_first_use=True,
    )

    # Configure stdlib logging to route through structlog's formatter
    formatter = structlog.stdlib.ProcessorFormatter(
        foreign_pre_chain=shared_processors,
        processors=[
            structlog.stdlib.ProcessorFormatter.remove_processors_meta,
            (
                structlog.processors.JSONRenderer()
                if json_output
                else structlog.dev.ConsoleRenderer(colors=sys.stderr.isatty())
            ),
        ],
    )

    handler = logging.StreamHandler()
    handler.setFormatter(formatter)
    root_logger = logging.getLogger()
    root_logger.handlers.clear()
    root_logger.addHandler(handler)
    root_logger.setLevel(getattr(logging, level.upper(), logging.INFO))

    _CONFIGURED = True


def get_logger(name: str | None = None) -> Any:
    """Get a structlog logger (or stdlib logger if structlog unavailable)."""
    if not _CONFIGURED:
        configure_logging()
    if HAS_STRUCTLOG:
        return structlog.stdlib.get_logger(name)
    return logging.getLogger(name)


def bind_context(**kwargs: Any) -> None:
    """Bind key-value pairs to the current context (async-safe via contextvars).

    All subsequent log lines in this coroutine will include these fields.
    Clear with clear_context() when done (e.g. at end of incident processing).
    """
    if HAS_STRUCTLOG:
        structlog.contextvars.bind_contextvars(**kwargs)


def clear_context() -> None:
    """Clear all context vars bound by bind_context()."""
    if HAS_STRUCTLOG:
        structlog.contextvars.clear_contextvars()
