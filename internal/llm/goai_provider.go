package llm

// goai_provider.go maps Sentinel's LLMProvider string to GoAI's
// provider-specific packages. Each provider handles its own auth scheme,
// request format, and response parsing.

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

// providerCfg holds the per-call GoAI model + options for one provider.
type providerCfg struct {
	providerType  string
	baseURL       string
	apiKey        string
	bedrockRegion string
}

// buildGoAIModel creates a GoAI LanguageModel for pc + modelName. Native
// providers (anthropic, bedrock, azure, google, vertex) use their own
// packages; OpenAI-compatible providers (zai, custom, litellm, ollama,
// vllm, ...) use the openai package with a custom base URL.
func buildGoAIModel(pc providerCfg, modelName string) provider.LanguageModel {
	switch strings.ToLower(pc.providerType) {
	case "anthropic":
		return anthropic.Chat(modelName, anthropic.WithAPIKey(pc.apiKey))
	case "bedrock":
		opts := []bedrock.Option{bedrock.WithAccessKey(pc.apiKey)}
		if pc.bedrockRegion != "" {
			opts = append(opts, bedrock.WithRegion(pc.bedrockRegion))
		}
		return bedrock.Chat(modelName, opts...)
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

	case "openai":
		return openai.Chat(modelName, openai.WithAPIKey(pc.apiKey))
	case "zai":
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

	case "custom", "litellm", "":
		opts := []openai.Option{openai.WithAPIKey(pc.apiKey)}
		if pc.baseURL != "" {
			opts = append(opts, openai.WithBaseURL(pc.baseURL))
		}
		return openai.Chat(modelName, opts...)

	default:
		opts := []openai.Option{openai.WithAPIKey(pc.apiKey)}
		if pc.baseURL != "" {
			opts = append(opts, openai.WithBaseURL(pc.baseURL))
		}
		return openai.Chat(modelName, opts...)
	}
}
