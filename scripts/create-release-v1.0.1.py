"""Create the v1.0.1 release with the updated Helm chart."""
import os
from github import Github

GITHUB_TOKEN = os.environ["GITHUB_TOKEN"]
gh = Github(GITHUB_TOKEN)
repo = gh.get_repo("karimzakzoukz/sentinel")

print("Creating release v1.0.1...")
release = repo.create_git_release(
    tag="v1.0.1",
    name="Sentinel v1.0.1 — Multi-LLM provider support",
    message="""# Sentinel v1.0.1 — Multi-LLM provider support

## What's new

Sentinel now supports **any** LLM provider out of the box. Set `sentinel.llm.provider` to one of:

- `openai` — GPT-4o, GPT-4o-mini
- `zai` — GLM-4.5, GLM-4-flash (default)
- `gemini` — Gemini 2.0 Flash, Gemini 2.5 Pro (native OpenAI endpoint)
- `ollama` — local models, fully air-gapped
- `vllm` — self-hosted HuggingFace models
- `litellm` — for **Anthropic Claude**, **AWS Bedrock**, or any non-OpenAI provider. Auto-deploys a LiteLLM sidecar that translates the calls.
- `azure` — Azure OpenAI
- `custom` — any OpenAI-compatible endpoint

## Install with any provider

### OpenAI
```bash
helm install sentinel https://github.com/karimzakzoukz/sentinel/releases/download/v1.0.1/sentinel-1.0.1.tgz \\
  --namespace sentinel --create-namespace \\
  --set sentinel.githubToken=$GITHUB_TOKEN \\
  --set sentinel.llm.provider=openai \\
  --set sentinel.llm.apiKey=$OPENAI_API_KEY \\
  --set sentinel.llm.fastModel=gpt-4o-mini \\
  --set sentinel.llm.frontierModel=gpt-4o
```

### Gemini
```bash
helm install sentinel https://github.com/karimzakzoukz/sentinel/releases/download/v1.0.1/sentinel-1.0.1.tgz \\
  --namespace sentinel --create-namespace \\
  --set sentinel.githubToken=$GITHUB_TOKEN \\
  --set sentinel.llm.provider=gemini \\
  --set sentinel.llm.apiKey=$GEMINI_API_KEY \\
  --set sentinel.llm.fastModel=gemini-2.0-flash \\
  --set sentinel.llm.frontierModel=gemini-2.5-pro
```

### Anthropic Claude (via auto-deployed LiteLLM sidecar)
```bash
helm install sentinel https://github.com/karimzakzoukz/sentinel/releases/download/v1.0.1/sentinel-1.0.1.tgz \\
  --namespace sentinel --create-namespace \\
  --set sentinel.githubToken=$GITHUB_TOKEN \\
  --set sentinel.llm.provider=litellm \\
  --set sentinel.llm.apiKey=$ANTHROPIC_API_KEY \\
  --set sentinel.llm.fastModel=anthropic/claude-3-5-haiku-20241022 \\
  --set sentinel.llm.frontierModel=anthropic/claude-3-5-sonnet-20241022
```

See the README for Ollama, vLLM, Azure, Bedrock, and custom provider instructions.

## Test coverage
- 50 unit tests pass (10 new tests for provider URL resolution)
- `helm lint` passes
- `helm template` produces valid K8s manifests for every provider
""",
    draft=False,
    prerelease=False,
)

print(f"Uploading Helm chart...")
release.upload_asset(
    path="/home/z/my-project/download/sentinel-1.0.1.tgz",
    name="sentinel-1.0.1.tgz",
    content_type="application/gzip",
)
print(f"Done: {release.html_url}")
