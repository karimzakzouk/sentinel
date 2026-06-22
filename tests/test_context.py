"""Context fetcher tests — trace parsing, file extraction."""
import os
import sys
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

# We can't fully test the ContextFetcher without external services, but we
# can test the regex parsing logic in isolation.
from reasoning.context import ContextFetcher, TRACE_FILE_RE


class TestTraceParsing:
    """The regex `([a-zA-Z0-9_.-]+\\.[a-zA-Z]{1,5})` is the contract."""

    def test_extracts_python_files(self):
        cf = ContextFetcher(settings=MagicMock(), llm_router=MagicMock())
        trace = """Traceback (most recent call last):
  File "src/payment/handler.py", line 42, in handle_payment
    process_charge(amount)
  File "src/payment/charge.py", line 18, in process_charge
    raise PaymentError("declined")
"""
        files = cf.parse_stack_trace(trace)
        assert "src/payment/handler.py" in files
        assert "src/payment/charge.py" in files

    def test_extracts_go_files(self):
        cf = ContextFetcher(settings=MagicMock(), llm_router=MagicMock())
        trace = """panic: runtime error: index out of range
goroutine 1 [running]:
main.handleRequest(...)
    /app/pkg/api/handler.go:47 +0x394
main.main()
    /app/cmd/server/main.go:23 +0x1c8"""
        files = cf.parse_stack_trace(trace)
        # The parser strips leading / but does NOT strip WORKDIR prefixes.
        # /app/pkg/api/handler.go → app/pkg/api/handler.go
        # The fetch_file_from_repo() method tries progressively shorter paths
        # to find the file in the repo (app/pkg/api/handler.go → pkg/api/handler.go → ...).
        assert "app/pkg/api/handler.go" in files
        assert "app/cmd/server/main.go" in files

    def test_extracts_java_files(self):
        cf = ContextFetcher(settings=MagicMock(), llm_router=MagicMock())
        trace = """java.lang.NullPointerException
    at com.acme.payment.ChargeHandler.handle(ChargeHandler.java:42)
    at com.acme.api.Router.dispatch(Router.java:18)"""
        files = cf.parse_stack_trace(trace)
        # The regex captures dot-separated paths. With the spec's regex
        # `([a-zA-Z0-9_.-]+\.[a-zA-Z]{1,5})`, the Java stack trace tokens
        # get captured as dotted strings. We just need to verify that
        # Java-style references are picked up.
        joined = " ".join(files)
        assert "ChargeHandler" in joined
        assert "Router" in joined

    def test_extracts_rust_files(self):
        cf = ContextFetcher(settings=MagicMock(), llm_router=MagicMock())
        trace = """thread 'main' panicked at 'index out of bounds',
src/payment/processor.rs:42:5"""
        files = cf.parse_stack_trace(trace)
        assert "src/payment/processor.rs" in files

    def test_excludes_image_files(self):
        cf = ContextFetcher(settings=MagicMock(), llm_router=MagicMock())
        text = "loaded logo.png and avatar.jpg"
        files = cf.parse_stack_trace(text)
        assert "logo.png" not in files
        assert "avatar.jpg" not in files

    def test_excludes_urls(self):
        cf = ContextFetcher(settings=MagicMock(), llm_router=MagicMock())
        text = "fetched https://example.com/api/users.json"
        files = cf.parse_stack_trace(text)
        # URLs should be excluded
        assert all(not f.startswith("http") for f in files), f"Found URL in {files}"

    def test_dedupes_files(self):
        cf = ContextFetcher(settings=MagicMock(), llm_router=MagicMock())
        trace = "File src/api.py:1\nFile src/api.py:2\nFile src/api.py:3"
        files = cf.parse_stack_trace(trace)
        assert files.count("src/api.py") == 1

    def test_regex_matches_expected_extensions(self):
        # Direct regex test — the spec calls this out specifically
        text = "src/api.py handler.go main.rs Service.java"
        matches = TRACE_FILE_RE.findall(text)
        # Should match all 4
        assert "src/api.py" in matches
        assert "handler.go" in matches
        assert "main.rs" in matches
        assert "Service.java" in matches
