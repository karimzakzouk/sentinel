package llmcost

// Pricing for common LLM models, in USD per 1K tokens. Index 0 = input,
// index 1 = output. Prices are public list prices as of late 2024 and
// are used only for cost estimation — they do NOT drive billing.
//
// Operators who negotiate custom pricing can override entries by adding
// them to PricingTable before the first Complete() call (the map is
// read-only after construction in practice).
var PricingTable = map[string][2]float64{
	// OpenAI
	"gpt-4o":        {0.0025, 0.01},
	"gpt-4o-mini":   {0.00015, 0.0006},
	"gpt-4.1":       {0.002, 0.008},
	"gpt-4.1-mini":  {0.0004, 0.0016},
	"gpt-4-turbo":   {0.01, 0.03},
	"gpt-4":         {0.03, 0.06},
	"gpt-3.5-turbo": {0.0005, 0.0015},

	// Anthropic
	"claude-3-opus":     {0.015, 0.075},
	"claude-3-sonnet":   {0.003, 0.015},
	"claude-3.5-sonnet": {0.003, 0.015},
	"claude-3-haiku":    {0.00025, 0.00125},

	// GLM / Z.AI
	"glm-4":       {0.002, 0.006},
	"glm-4-flash": {0.0001, 0.0001},
	"glm-4.5":     {0.005, 0.015},

	// Google
	"gemini-1.5-pro":   {0.00125, 0.005},
	"gemini-1.5-flash": {0.000075, 0.0003},

	// Local / Ollama (free, but cost is non-zero in compute)
	"llama3":  {0, 0},
	"mistral": {0, 0},
	"qwen2":   {0, 0},
}

// DefaultInputPer1K is the fallback USD cost per 1K input tokens for
// models not in PricingTable.
const DefaultInputPer1K = 0.001

// DefaultOutputPer1K is the fallback USD cost per 1K output tokens for
// models not in PricingTable.
const DefaultOutputPer1K = 0.002

// costPerTokens returns the estimated USD cost for inputTokens +
// outputTokens on model. Unknown models fall back to
// DefaultInputPer1K / DefaultOutputPer1K so the estimator never returns
// zero for a real LLM call (which would under-report cost in dashboards).
func costPerTokens(model string, inputTokens, outputTokens int) float64 {
	p, ok := PricingTable[model]
	if !ok {
		return float64(inputTokens)/1000.0*DefaultInputPer1K +
			float64(outputTokens)/1000.0*DefaultOutputPer1K
	}
	return float64(inputTokens)/1000.0*p[0] +
		float64(outputTokens)/1000.0*p[1]
}
