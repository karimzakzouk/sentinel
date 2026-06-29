# Sentinel — Community & Support

Welcome! This document covers how to get help, contribute, and engage with the Sentinel community.

## Getting help

| If you... | Then... |
|---|---|
| Found a bug | [Open a bug report](https://github.com/karimzakzouk/sentinel/issues/new?assignees=&labels=bug%2Ctriage&template=bug_report.yml) |
| Have a feature idea | [Open a feature request](https://github.com/karimzakzouk/sentinel/issues/new?assignees=&labels=enhancement%2Ctriage&template=feature_request.yml) |
| Want to share how Sentinel performed during a real incident | [Open an incident retro](https://github.com/karimzakzouk/sentinel/issues/new?assignees=&labels=incident-retro&template=incident_retro.yml) |
| Found a security vulnerability | [Report it privately](SECURITY.md) — **do NOT open a public issue** |
| Have a question | [Start a GitHub Discussion](https://github.com/karimzakzouk/sentinel/discussions/new) |
| Want to chat with other users | (Discord/Slack coming soon — for now, use Discussions) |

## Contributing

See [CONTRIBUTING.md](../CONTRIBUTING.md) for:
- Quick start (build, test, lint, run locally)
- Architecture overview
- How to add a new error pattern / LLM provider / agent
- CI workflow
- How to release a new version

## Community channels

- **GitHub Issues** — bugs, features, incident retros
- **GitHub Discussions** — questions, show & tell, ideas
- **GitHub Releases** — subscribe to get notified of new versions
- **Helm chart** — `helm repo add sentinel https://karimzakzouk.github.io/sentinel/`

## Code of Conduct

Participation in this community is governed by the [Code of Conduct](CODE_OF_CONDUCT.md). Please be excellent to each other.

## Acknowledgments

Sentinel builds on the work of many open-source projects:
- [go-git](https://github.com/go-git/go-git) — pure-Go Git implementation
- [GoAI](https://github.com/zendev-sh/goai) — multi-provider LLM SDK
- [HNSW](https://github.com/coder/hnsw) — vector similarity search
- [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) — pure-Go SQLite
- [shadcn/ui](https://ui.shadcn.com) — web UI components
- [lucide-react](https://lucide.dev) — icons

And draws inspiration from:
- [Aider](https://aider.chat) — SEARCH/REPLACE block format
- [SWE-agent](https://swe-agent.com) — agent loop design
- [Prow](https://docs.prow.k8s.io) — chatops command UX
- [Robusta](https://robusta.dev) — K8s troubleshooting patterns
