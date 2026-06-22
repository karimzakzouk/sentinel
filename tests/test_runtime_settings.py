"""Tests for the runtime settings module (Pydantic Settings v2 model)."""

from runtime_settings import RuntimeSettings, SettingsUpdate, apply_runtime_to_settings, load_from_env

from sentinel_common.config import Settings


class TestRuntimeSettings:
    def test_load_from_env_picks_up_defaults(self, monkeypatch):
        monkeypatch.delenv("SENTINEL_LLM_PROVIDER", raising=False)
        monkeypatch.delenv("SENTINEL_LLM_FAST_MODEL", raising=False)
        rs = load_from_env()
        assert rs.llm_provider == "zai"
        assert rs.llm_fast_model == "glm-4-flash"

    def test_load_from_env_reads_overrides(self, monkeypatch):
        monkeypatch.setenv("SENTINEL_LLM_PROVIDER", "openai")
        monkeypatch.setenv("SENTINEL_LLM_FAST_MODEL", "gpt-4o-mini")
        monkeypatch.setenv("SENTINEL_GITHUB_TOKEN", "ghp_test")
        rs = load_from_env()
        assert rs.llm_provider == "openai"
        assert rs.llm_fast_model == "gpt-4o-mini"
        assert rs.github_token == "ghp_test"

    def test_to_masked_dict_masks_secrets(self):
        rs = RuntimeSettings(github_token="ghp_supersecret123", llm_api_key="sk-secret")
        d = rs.to_masked_dict()
        assert d["github_token"].startswith("ghp_")
        assert d["github_token"].endswith("***")
        assert "***" in d["llm_api_key"]
        assert d["github_token_set"] is True
        assert d["llm_api_key_set"] is True

    def test_to_masked_dict_handles_short_secrets(self):
        rs = RuntimeSettings(github_token="ab", llm_api_key="cd")
        d = rs.to_masked_dict()
        # Short secrets just show ***
        assert d["github_token"] == "***"
        assert d["llm_api_key"] == "***"

    def test_to_masked_dict_shows_set_flag_when_empty(self):
        rs = RuntimeSettings()
        d = rs.to_masked_dict()
        assert d["github_token_set"] is False
        assert d["llm_api_key_set"] is False


class TestApplyUpdate:
    def test_applies_known_field(self):
        rs = RuntimeSettings()
        changed = rs.apply_update({"llm_provider": "openai"})
        assert "llm_provider" in changed
        assert rs.llm_provider == "openai"

    def test_skips_none_values(self):
        rs = RuntimeSettings()
        update = SettingsUpdate(llm_provider=None)
        changed = rs.apply_update(update.model_dump(exclude_unset=True))
        assert changed == []

    def test_skips_masked_secrets(self):
        """A masked value (ending ***) must NOT overwrite the real secret."""
        rs = RuntimeSettings(github_token="ghp_realsecret")
        changed = rs.apply_update({"github_token": "ghp_***"})
        assert changed == []
        assert rs.github_token == "ghp_realsecret"

    def test_applies_real_secret(self):
        rs = RuntimeSettings(github_token="ghp_old")
        changed = rs.apply_update({"github_token": "ghp_new"})
        assert "github_token" in changed
        assert rs.github_token == "ghp_new"

    def test_skips_unknown_fields(self):
        rs = RuntimeSettings()
        changed = rs.apply_update({"unknown_field": "value"})
        assert changed == []

    def test_only_returns_changed_fields(self):
        rs = RuntimeSettings(llm_provider="openai")
        changed = rs.apply_update(
            {
                "llm_provider": "openai",  # unchanged
                "llm_fast_model": "gpt-4o",  # changed
            }
        )
        assert changed == ["llm_fast_model"]


class TestApplyRuntimeToSettings:
    def test_syncs_runtime_to_settings(self):
        runtime = RuntimeSettings(
            github_token="ghp_test",
            llm_provider="openai",
            llm_fast_model="gpt-4o",
            llm_frontier_model="gpt-4o",
            production_namespaces="prod,staging",
            write_namespaces="sim",
        )
        settings = Settings()
        apply_runtime_to_settings(runtime, settings)
        assert settings.github_token == "ghp_test"
        assert settings.llm_provider == "openai"
        assert settings.llm_fast_model == "gpt-4o"
        assert settings.production_namespaces == "prod,staging"
        assert settings.write_namespaces == "sim"
