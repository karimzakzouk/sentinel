"""Tests for the namespace write-allowlist — verifies Sentinel denies fixes
in namespaces not listed in writeNamespaces."""
import os
from sentinel_common.config import Settings


def test_empty_write_namespaces_is_read_only():
    """When writeNamespaces is empty, Sentinel cannot write anywhere."""
    os.environ.pop("SENTINEL_WRITE_NAMESPACES", None)
    s = Settings()
    assert s.write_namespace_list == []
    assert s.can_write_to("prod") is False
    assert s.can_write_to("default") is False
    assert s.can_write_to("anything") is False


def test_write_namespaces_allows_listed():
    os.environ["SENTINEL_WRITE_NAMESPACES"] = "prod,staging"
    s = Settings()
    assert s.can_write_to("prod") is True
    assert s.can_write_to("staging") is True
    assert s.can_write_to("default") is False
    assert s.can_write_to("dev") is False


def test_write_namespaces_handles_whitespace():
    os.environ["SENTINEL_WRITE_NAMESPACES"] = " prod , staging , "
    s = Settings()
    assert s.can_write_to("prod") is True
    assert s.can_write_to("staging") is True
    assert "prod" in s.write_namespace_list
    assert "staging" in s.write_namespace_list
    assert "" not in s.write_namespace_list
