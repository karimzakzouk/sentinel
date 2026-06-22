"""Create the v1.0.0 release with the Helm chart as an asset."""
import os
from github import Github

GITHUB_TOKEN = os.environ["GITHUB_TOKEN"]
gh = Github(GITHUB_TOKEN)
repo = gh.get_repo("karimzakzoukz/sentinel")

print("Creating release v1.0.0...")
release = repo.create_git_release(
    tag="v1.0.0",
    name="Sentinel v1.0.0 — Initial Release",
    message="""# Sentinel v1.0.0

The Autonomous AI Site Reliability Engineer.

## Installation

```bash
# Download the chart
wget https://github.com/karimzakzoukz/sentinel/releases/download/v1.0.0/sentinel-1.0.0.tgz

# Install
helm install sentinel ./sentinel-1.0.0.tgz \\
  --namespace sentinel \\
  --create-namespace \\
  --set sentinel.githubToken=ghp_xxx \\
  --set sentinel.llm.apiKey=sk-xxx

# Watch it come up
kubectl -n sentinel get pods -w

# Access the UI
kubectl -n sentinel port-forward svc/sentinel-ui 8080:80
```

## What's included

- All 7 Python services (ingestion, detection, reasoning, safety, execution, feedback, operator)
- Next.js 16 Jenkins-style UI
- Helm chart with Strimzi Kafka, Postgres, Weaviate, OPA
- SentinelIncident CRD
- 4 Rego policy files
- 40+ unit tests + live GitHub integration test (actually opens a real PR on your account)

See the README for full architecture + configuration docs.
""",
    draft=False,
    prerelease=False,
)

print(f"Uploading Helm chart...")
release.upload_asset(
    path="/home/z/my-project/download/sentinel-1.0.0.tgz",
    name="sentinel-1.0.0.tgz",
    content_type="application/gzip",
)
print(f"Done: {release.html_url}")
