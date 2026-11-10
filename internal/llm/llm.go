// Package llm asks a language model for text, through the two request
// formats nearly every provider and local server speaks: OpenAI's chat
// completions (OpenAI, Azure, Gemini, Mistral, DeepSeek, Groq, xAI,
// OpenRouter, Ollama, vLLM, llama.cpp, LM Studio, LiteLLM) and Anthropic's
// messages. Nothing here acts on a cluster: a model's text is a candidate
// the Fixer checks, the Lab judges and a person merges.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Format is a request format.
type Format string

const (
	// OpenAI is the chat completions format.
	OpenAI Format = "openai"
	// Anthropic is the messages format.
	Anthropic Format = "anthropic"
)

// Client asks one model.
type Client struct {
	Format Format
	// URL is the API's base, such as https://api.openai.com/v1,
	// http://ollama.ollama:11434/v1 or https://api.anthropic.com/v1.
	URL   string
	Model string
	// Key, when set, is sent as the format expects: a bearer token, or
	// Anthropic's x-api-key.
	Key       string
	MaxTokens int
	HTTP      *http.Client
}

// Complete sends a system prompt and a request, and returns the reply.
func (c *Client) Complete(ctx context.Context, system, user string) (string, error) {
	maxTokens := c.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	var body any
	var path string
	switch c.Format {
	case Anthropic:
		path = "/messages"
		body = map[string]any{"model": c.Model, "system": system, "max_tokens": maxTokens, "temperature": 0,
			"messages": []map[string]string{{"role": "user", "content": user}}}
	case OpenAI, "":
		path = "/chat/completions"
		body = map[string]any{"model": c.Model, "max_tokens": maxTokens, "temperature": 0,
			"messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}}}
	default:
		return "", fmt.Errorf("unknown model API format %q: use openai or anthropic", c.Format)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(c.URL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	switch {
	case c.Format == Anthropic:
		req.Header.Set("anthropic-version", "2023-06-01")
		if c.Key != "" {
			req.Header.Set("x-api-key", c.Key)
		}
	case c.Key != "":
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Minute}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("ask %s: %w", c.Model, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("the model API answered %s: %s", resp.Status, excerpt(data, c.Key))
	}
	return reply(c.Format, data)
}

func reply(format Format, data []byte) (string, error) {
	if format == Anthropic {
		var r struct {
			Content []struct {
				Type, Text string
			} `json:"content"`
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return "", fmt.Errorf("read the model's reply: %w", err)
		}
		var parts []string
		for _, c := range r.Content {
			if c.Type == "text" {
				parts = append(parts, c.Text)
			}
		}
		if len(parts) == 0 {
			return "", errors.New("the model's reply held no text")
		}
		return strings.Join(parts, ""), nil
	}
	var r struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return "", fmt.Errorf("read the model's reply: %w", err)
	}
	if len(r.Choices) == 0 || r.Choices[0].Message.Content == "" {
		return "", errors.New("the model's reply held no text")
	}
	return r.Choices[0].Message.Content, nil
}

// excerpt is the start of an error body, with the key never in it.
func excerpt(data []byte, key string) string {
	s := string(data)
	if key != "" {
		s = strings.ReplaceAll(s, key, "[key]")
	}
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}
