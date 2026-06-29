package llm

// goai_provider.go maps Sentinel's cfg.LLMProvider string to GoAI's
// provider-specific packages. Each provider handles its own auth scheme,
// request format, and response parsing — no proxy needed.
// Replaces the previous OpenAI-only HTTP router with GoAI
// (github.com/zendev-sh/goai), a pure-Go SDK with 25+ native providers.
// This adds native support for Anthropic (/v1/messages + x-api-key),
// AWS Bedrock (SigV4), Azure OpenAI (api-key + api-version), Google
// Vertex/Gemini, and 20+ others — all in a single binary, no Python,
// no sidecar, no proxy.
// Binary size impact: +6MB (GoAI + deps, stdlib-only).
// The existing Router public API (FrontierJSON, FastJSON, FrontierText,
// FastText, Update, Snapshot, SetRetryConfig) is preserved — the agent
// pipeline doesn't change at all.


import (
	"strings"

	"github.com/zendev-sh/goai/provider"
	"github.com/zendev-sh/goai/provider/anthropic"
	"github.com/zendev-sh/goai/provider/azure"
	"github.com/zendev-sh/goai/provider/bedrock"
	"github.com/zendev-sh/goai/provider/cerebras"
	"github.com/zendev-sh/goai/provider/cohere"
	"github.com/zendev-sh/goai/provider/deepseek"
	"github.com/zendev-sh/goai/provider/fireworks"
	"github.com/zendev-sh/goai/provider/google"
	"github.com/zendev-sh/goai/provider/groq"
	"github.com/zendev-sh/goai/provider/mistral"
	"github.com/zendev-sh/goai/provider/ollama"
	"github.com/zendev-sh/goai/provider/openai"
	"github.com/zendev-sh/goai/provider/openrouter"
	"github.com/zendev-sh/goai/provider/perplexity"
	"github.com/zendev-sh/goai/provider/together"
	"github.com/zendev-sh/goai/provider/vertex"
	"github.com/zendev-sh/goai/provider/vllm"
	"github.com/zendev-sh/goai/provider/xai"
)

// providerConfig holds the GoAI model + options for a specific LLM provider.
// Built once per Router config change, used for every LLM call.

type providerConfig struct {
	providerType string
	baseURL      string
	apiKey       string
}

// buildGoAIModel creates a GoAI LanguageModel for the given provider + model name.
// The provider string maps to GoAI's provider packages. For OpenAI-compatible
// providers (zai, custom, litellm, ollama, vllm), we use the openai provider
// package with a custom base URL — they all speak the same API surface.

func (r *Router) buildGoAIModel(modelName string) provider.LanguageModel {
	pc := r.providerSnapshot()

	switch strings.ToLower(pc.providerType) {
	// --- Native providers (not OpenAI-compatible) ---

	case "anthropic":
		return anthropic.Chat(modelName, anthropic.WithAPIKey(pc.apiKey))
	case "bedrock":
		return bedrock.Chat(modelName,
			bedrock.WithAccessKey(pc.apiKey),
			bedrock.WithRegion(func() string { r.mu.RLock(); defer r.mu.RUnlock(); return r.bedrockRegion }()),
		)
	case "azure":
		opts := []azure.Option{azure.WithAPIKey(pc.apiKey)}
		if pc.baseURL != "" {
			opts = append(opts, azure.WithEndpoint(pc.baseURL))
		}
		return azure.Chat(modelName, opts...)
	case "google", "gemini":
		return google.Chat(modelName, google.WithAPIKey(pc.apiKey))
	case "vertex":
		return vertex.Chat(modelName, vertex.WithAPIKey(pc.apiKey))

	// --- OpenAI-compatible providers (use openai package with custom base URL) ---

	case "openai":
		return openai.Chat(modelName, openai.WithAPIKey(pc.apiKey))
	case "zai":
		// z.ai speaks OpenAI-compatible API at https://api.z.ai/api/paas/v4

		baseURL := pc.baseURL
		if baseURL == "" {
			baseURL = "https://api.z.ai/api/paas/v4"
		}
		return openai.Chat(modelName,
			openai.WithAPIKey(pc.apiKey),
			openai.WithBaseURL(baseURL),
		)
	case "openrouter":
		return openrouter.Chat(modelName, openrouter.WithAPIKey(pc.apiKey))
	case "groq":
		return groq.Chat(modelName, groq.WithAPIKey(pc.apiKey))
	case "mistral":
		return mistral.Chat(modelName, mistral.WithAPIKey(pc.apiKey))
	case "cohere":
		return cohere.Chat(modelName, cohere.WithAPIKey(pc.apiKey))
	case "xai":
		return xai.Chat(modelName, xai.WithAPIKey(pc.apiKey))
	case "deepseek":
		return deepseek.Chat(modelName, deepseek.WithAPIKey(pc.apiKey))
	case "cerebras":
		return cerebras.Chat(modelName, cerebras.WithAPIKey(pc.apiKey))
	case "fireworks":
		return fireworks.Chat(modelName, fireworks.WithAPIKey(pc.apiKey))
	case "together":
		return together.Chat(modelName, together.WithAPIKey(pc.apiKey))
	case "perplexity":
		return perplexity.Chat(modelName, perplexity.WithAPIKey(pc.apiKey))
	case "ollama":
		return ollama.Chat(modelName, ollama.WithBaseURL(pc.baseURL))
	case "vllm":
		return vllm.Chat(modelName, vllm.WithAPIKey(pc.apiKey), vllm.WithBaseURL(pc.baseURL))

	// --- Custom / unknown: treat as OpenAI-compatible with custom base URL ---

	case "custom", "litellm", "":
		opts := []openai.Option{openai.WithAPIKey(pc.apiKey)}
		if pc.baseURL != "" {
			opts = append(opts, openai.WithBaseURL(pc.baseURL))
		}
		return openai.Chat(modelName, opts...)

	default:
		// Unknown provider — fall back to OpenAI-compatible with custom base URL.
		// This is the safest default since most LLM providers expose an OpenAI-compat endpoint.

		opts := []openai.Option{openai.WithAPIKey(pc.apiKey)}
		if pc.baseURL != "" {
			opts = append(opts, openai.WithBaseURL(pc.baseURL))
		}
		return openai.Chat(modelName, opts...)
	}
}
