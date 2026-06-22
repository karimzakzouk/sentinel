// Package llm provides a minimal OpenAI-compatible LLM client.
// No SDK — just net/http. Works with OpenAI, z.ai, OpenRouter, Gemini-via-OpenAI,
// Ollama, vLLM, LiteLLM, any OpenAI-compatible endpoint.
package llm

import (
        "bytes"
        "context"
        "encoding/json"
        "fmt"
        "io"
        "net/http"
        "regexp"
        "strings"
        "time"
)

// Router picks fast vs frontier model and centralizes the API client.
type Router struct {
        baseURL      string
        apiKey       string
        fastModel    string
        frontierModel string
        http         *http.Client
}

// New creates a Router.
func New(baseURL, apiKey, fastModel, frontierModel string) *Router {
        return &Router{
                baseURL:       strings.TrimRight(baseURL, "/"),
                apiKey:        apiKey,
                fastModel:     fastModel,
                frontierModel: frontierModel,
                http: &http.Client{
                        Timeout: 120 * time.Second,
                },
        }
}

// Update reconfigures the router (called when settings change at runtime).
func (r *Router) Update(baseURL, apiKey, fastModel, frontierModel string) {
        r.baseURL = strings.TrimRight(baseURL, "/")
        r.apiKey = apiKey
        r.fastModel = fastModel
        r.frontierModel = frontierModel
}

// FastJSON calls the fast model and parses JSON output.
func (r *Router) FastJSON(ctx context.Context, system, user string, maxTokens int) (map[string]any, error) {
        return r.completeJSON(ctx, r.fastModel, system, user, maxTokens)
}

// FrontierJSON calls the frontier model and parses JSON output.
func (r *Router) FrontierJSON(ctx context.Context, system, user string, maxTokens int) (map[string]any, error) {
        return r.completeJSON(ctx, r.frontierModel, system, user, maxTokens)
}

// FastText calls the fast model and returns raw text.
func (r *Router) FastText(ctx context.Context, system, user string, maxTokens int) (string, error) {
        return r.complete(ctx, r.fastModel, system, user, maxTokens)
}

// FrontierText calls the frontier model and returns raw text.
func (r *Router) FrontierText(ctx context.Context, system, user string, maxTokens int) (string, error) {
        return r.complete(ctx, r.frontierModel, system, user, maxTokens)
}

type chatRequest struct {
        Model          string         `json:"model"`
        Messages       []chatMessage  `json:"messages"`
        Temperature    float64        `json:"temperature"`
        MaxTokens      int            `json:"max_tokens,omitempty"`
        ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type chatMessage struct {
        Role    string `json:"role"`
        Content string `json:"content"`
}

type responseFormat struct {
        Type string `json:"type"`
}

type chatResponse struct {
        Choices []struct {
                Message chatMessage `json:"message"`
        } `json:"choices"`
        Error *struct {
                Message string `json:"message"`
                Type    string `json:"type"`
        } `json:"error,omitempty"`
}

func (r *Router) complete(ctx context.Context, model, system, user string, maxTokens int) (string, error) {
        req := chatRequest{
                Model: model,
                Messages: []chatMessage{
                        {Role: "system", Content: system},
                        {Role: "user", Content: user},
                },
                Temperature: 0.2,
                MaxTokens:   maxTokens,
        }
        body, err := r.do(ctx, req)
        if err != nil {
                return "", err
        }
        if len(body.Choices) == 0 {
                return "", fmt.Errorf("LLM returned no choices")
        }
        return body.Choices[0].Message.Content, nil
}

func (r *Router) completeJSON(ctx context.Context, model, system, user string, maxTokens int) (map[string]any, error) {
        prompt := user + "\n\nRespond with a single valid JSON object. No prose, no markdown fences."
        req := chatRequest{
                Model: model,
                Messages: []chatMessage{
                        {Role: "system", Content: system},
                        {Role: "user", Content: prompt},
                },
                Temperature: 0.1,
                MaxTokens:   maxTokens,
                // Try JSON mode first; if the provider rejects it we retry without.
                ResponseFormat: &responseFormat{Type: "json_object"},
        }
        body, err := r.do(ctx, req)
        if err != nil {
                // Retry without response_format (some providers don't support it)
                req.ResponseFormat = nil
                body, err = r.do(ctx, req)
                if err != nil {
                        return nil, err
                }
        }
        if len(body.Choices) == 0 {
                return map[string]any{}, nil
        }
        return extractJSON(body.Choices[0].Message.Content), nil
}

func (r *Router) do(ctx context.Context, req chatRequest) (*chatResponse, error) {
        body, err := json.Marshal(req)
        if err != nil {
                return nil, fmt.Errorf("marshal request: %w", err)
        }

        httpReq, err := http.NewRequestWithContext(ctx, "POST", r.baseURL+"/chat/completions", bytes.NewReader(body))
        if err != nil {
                return nil, fmt.Errorf("build request: %w", err)
        }
        httpReq.Header.Set("Content-Type", "application/json")
        httpReq.Header.Set("Authorization", "Bearer "+r.apiKey)

        resp, err := r.http.Do(httpReq)
        if err != nil {
                return nil, fmt.Errorf("LLM call failed: %w", err)
        }
        defer resp.Body.Close()

        respBody, err := io.ReadAll(resp.Body)
        if err != nil {
                return nil, fmt.Errorf("read response: %w", err)
        }

        if resp.StatusCode >= 400 {
                return nil, fmt.Errorf("LLM API returned %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 500)]))
        }

        var chatResp chatResponse
        if err := json.Unmarshal(respBody, &chatResp); err != nil {
                return nil, fmt.Errorf("unmarshal response: %w", err)
        }
        if chatResp.Error != nil {
                return nil, fmt.Errorf("LLM error (%s): %s", chatResp.Error.Type, chatResp.Error.Message)
        }
        return &chatResp, nil
}

// extractJSON defensively parses JSON from LLM output.
// Handles markdown fences, bare JSON, prose around JSON, and truncated JSON.
func extractJSON(text string) map[string]any {
        // Try 1: markdown-fenced JSON block
        if m := jsonBlockRe.FindStringSubmatch(text); len(m) >= 2 {
                if v, ok := tryParse(m[1]); ok {
                        return v
                }
        }
        // Try 2: bare JSON object
        if m := bareJSONRe.FindStringSubmatch(text); len(m) >= 1 {
                if v, ok := tryParse(m[0]); ok {
                        return v
                }
        }
        // Try 3: the whole text
        if v, ok := tryParse(text); ok {
                return v
        }
        // Try 4: repair truncated JSON
        if repaired := repairTruncated(text); repaired != "" {
                if v, ok := tryParse(repaired); ok {
                        return v
                }
        }
        // Last resort: empty dict so callers don't crash
        return map[string]any{}
}

var (
        jsonBlockRe = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\}|\\[.*?\\])\\s*```")
        bareJSONRe  = regexp.MustCompile("(?s)(\\{.*\\}|\\[.*\\])")
)

func tryParse(s string) (map[string]any, bool) {
        var v map[string]any
        if err := json.Unmarshal([]byte(s), &v); err == nil {
                return v, true
        }
        return nil, false
}

// repairTruncated tries to close unclosed strings/braces/brackets in JSON
// that was cut off by max_tokens. Tracks opens as a stack and closes in
// reverse order.
func repairTruncated(text string) string {
        start := strings.Index(text, "{")
        if start == -1 {
                return ""
        }
        s := text[start:]

        inString := false
        escapeNext := false
        var stack []rune // tracks open braces/brackets in order

        for _, c := range s {
                if escapeNext {
                        escapeNext = false
                        continue
                }
                if c == '\\' {
                        escapeNext = true
                        continue
                }
                if c == '"' {
                        inString = !inString
                        continue
                }
                if inString {
                        continue
                }
                switch c {
                case '{', '[':
                        stack = append(stack, c)
                case '}':
                        if len(stack) > 0 && stack[len(stack)-1] == '{' {
                                stack = stack[:len(stack)-1]
                        }
                case ']':
                        if len(stack) > 0 && stack[len(stack)-1] == '[' {
                                stack = stack[:len(stack)-1]
                        }
                }
        }

        if inString {
                s += `"`
        }
        // Trim trailing comma
        s = strings.TrimRight(strings.TrimSpace(s), ",")
        // Close in reverse order: stack is [{, [, {] → close }, ], }
        for i := len(stack) - 1; i >= 0; i-- {
                if stack[i] == '{' {
                        s += "}"
                } else if stack[i] == '[' {
                        s += "]"
                }
        }
        return s
}

func min(a, b int) int {
        if a < b {
                return a
        }
        return b
}

func max(a, b int) int {
        if a > b {
                return a
        }
        return b
}
