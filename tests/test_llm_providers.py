"""Tests for LLM provider URL resolution."""

import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent.parent / "services" / "common"))

from sentinel_common.config import Settings, get_settings


def _make_settings(provider: str, base_url: str = "", api_key: str = "test") -> Settings:
    """Build a Settings instance with the given provider/base_url."""
    # Clear cached settings so env var changes take effect
    get_settings.cache_clear()
    os.environ["SENTINEL_LLM_PROVIDER"] = provider
    os.environ["SENTINEL_LLM_BASE_URL"] = base_url
    os.environ["SENTINEL_LLM_API_KEY"] = api_key
    return Settings()


def test_provider_openai_resolves_to_correct_url():
    s = _make_settings("openai")
    assert s.resolved_base_url == "https://api.openai.com/v1"


def test_provider_gemini_resolves_to_correct_url():
    s = _make_settings("gemini")
    assert s.resolved_base_url == "https://generativelanguage.googleapis.com/v1beta/openai"


def test_provider_zai_resolves_to_correct_url():
    s = _make_settings("zai")
    assert s.resolved_base_url == "https://api.z.ai/api/paas/v4"


def test_provider_ollama_resolves_to_correct_url():
    s = _make_settings("ollama")
    assert s.resolved_base_url == "http://ollama:11434/v1"


def test_provider_vllm_resolves_to_correct_url():
    s = _make_settings("vllm")
    assert s.resolved_base_url == "http://vllm:8000/v1"


def test_provider_litellm_resolves_to_correct_url():
    s = _make_settings("litellm")
    assert s.resolved_base_url == "http://litellm:4000/v1"


def test_explicit_base_url_overrides_provider():
    """If user sets baseUrl explicitly, that wins — even if provider is set."""
    s = _make_settings("openai", base_url="https://my-custom-proxy.example.com/v1")
    assert s.resolved_base_url == "https://my-custom-proxy.example.com/v1"


def test_provider_azure_falls_back_when_no_base_url():
    """Azure requires an explicit base URL — without one we fall back to z.ai."""
    s = _make_settings("azure")
    # Should not crash; falls back to z.ai with a warning
    assert s.resolved_base_url == "https://api.z.ai/api/paas/v4"


def test_provider_unknown_falls_back_gracefully():
    s = _make_settings("some-new-llm-company")
    assert s.resolved_base_url == "https://api.z.ai/api/paas/v4"


def test_provider_case_insensitive():
    s = _make_settings("OpenAI")
    assert s.resolved_base_url == "https://api.openai.com/v1"
    s = _make_settings("GEMINI")
    assert s.resolved_base_url == "https://generativelanguage.googleapis.com/v1beta/openai"
