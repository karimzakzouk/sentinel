package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/karimzakzouk/sentinel/internal/codeintel"
	"github.com/karimzakzouk/sentinel/internal/llm"
	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/repo"
)

const (
	DefaultMaxTurns         = 20
	DefaultTurnTimeout      = 30 * time.Second
	DefaultCompactThreshold = 10
	MaxFilePreviewLines     = 100

	// maxRetriesPerTurn is the number of correction attempts when the LLM
	// returns a response that can't be parsed as valid JSON. The retry
	// sends a correction prompt explaining the expected format. Does not
	// count as a turn — the agent loop continues from the same turn number.
	maxRetriesPerTurn = 2
)

// agentSystemRole is the system-level instruction sent with every LLM call.
// It enforces JSON-only output and describes the two valid response shapes.
const agentSystemRole = `You are an autonomous SRE agent investigating a production crash.

CRITICAL: Respond with ONLY a JSON object. No prose, no markdown, no explanation.
The JSON must be one of:

1. To call a tool:
   {"tool": "<tool_name>", "args": {<tool-specific args>}}

2. To declare the investigation complete:
   {"done": true, "root_cause": "<one-sentence root cause>", "confidence": 0.0-1.0, "files_to_change": ["path/to/file"]}

Do not include any text before or after the JSON object.`

// agentInstructions is sent as the first user message. It describes the
// investigation task and gives an example of the expected output format.
const agentInstructions = `Investigate the crash using the tools above.

When you have identified the root cause and the files that need to change,
respond with:
{"done": true, "root_cause": "...", "confidence": 0.85, "files_to_change": ["path/to/file"]}

Otherwise, call a tool to explore the codebase:
{"tool": "read_file", "args": {"path": "app/main.py", "start_line": 1}}

IMPORTANT: Your entire response must be a single JSON object — no other text.`

// correctionPrompt is sent when the LLM returns a response that couldn't
// be parsed as valid JSON. This is the retry-with-error-feedback pattern:
// the LLM sees its own bad output and is asked to correct it.
const correctionPrompt = `Your previous response was not a valid JSON object.

Please respond with ONLY a JSON object in one of these formats:

To call a tool: {"tool": "<name>", "args": {...}}
To finish: {"done": true, "root_cause": "...", "confidence": 0.85, "files_to_change": [...]}

Do not include any text before or after the JSON. Do not wrap it in markdown code fences.`

// Tool is a function the LLM can call during the agent loop.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  string `json:"parameters"`
}

// ToolResult is the output of a tool call.
type ToolResult struct {
	Tool    string `json:"tool"`
	Success bool   `json:"success"`
	Output  string `json:"output"`
}

// Turn is one iteration of the agent loop.
type Turn struct {
	TurnNumber  int           `json:"turn"`
	LLMResponse string        `json:"llm_response"`
	ToolCall    *ToolCall     `json:"tool_call,omitempty"`
	ToolResult  *ToolResult   `json:"tool_result,omitempty"`
	Duration    time.Duration `json:"duration"`
}

// ToolCall is the LLM's request to execute a tool.
type ToolCall struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
}

// AgentConfig configures the agent loop.
type AgentConfig struct {
	MaxTurns         int
	TurnTimeout      time.Duration
	CompactThreshold int
}

// Agent runs the investigation loop.
type Agent struct {
	router    *llm.Router
	cfg       AgentConfig
	tools     []Tool
	toolFuncs map[string]ToolFunc
}

// ToolFunc executes a tool and returns its output.
type ToolFunc func(ctx context.Context, args map[string]any) ToolResult

// InvestigationResult is the final output of the agent loop.
type InvestigationResult struct {
	RootCause   string   `json:"root_cause"`
	Confidence  float64  `json:"confidence"`
	SourceFiles []string `json:"source_files"`
	Hypotheses  []string `json:"hypotheses"`
	TurnsUsed   int      `json:"turns_used"`
	TotalTokens int      `json:"total_tokens"`
	Trace       []Turn   `json:"trace"`
}

func New(router *llm.Router, cfg AgentConfig) *Agent {
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = DefaultMaxTurns
	}
	if cfg.TurnTimeout <= 0 {
		cfg.TurnTimeout = DefaultTurnTimeout
	}
	if cfg.CompactThreshold <= 0 {
		cfg.CompactThreshold = DefaultCompactThreshold
	}
	return &Agent{
		router: router,
		cfg:    cfg,
	}
}

// RegisterTool adds a tool to the agent's toolset.
func (a *Agent) RegisterTool(name, description, parameters string, fn ToolFunc) {
	a.tools = append(a.tools, Tool{
		Name:        name,
		Description: description,
		Parameters:  parameters,
	})
	if a.toolFuncs == nil {
		a.toolFuncs = make(map[string]ToolFunc)
	}
	a.toolFuncs[name] = fn
}

// Run executes the agent loop. The system prompt sets up the investigation
// context (crash logs, repo info, symbol list). The LLM picks tools until
// it declares "done" or hits the turn cap.
//
// When the LLM returns a response that is not valid JSON (a common failure
// mode for smaller models like glm-4-flash), the agent sends a correction
// prompt asking the LLM to respond with only the JSON object. This retry
// does NOT count as a turn — the agent gets up to maxRetriesPerTurn (2)
// correction attempts before the turn is counted and the loop moves on.
// This is the RetryWithErrorOutputParser pattern from LangChain, adapted
// for our text-based tool calling.
func (a *Agent) Run(ctx context.Context, systemPrompt string) (*InvestigationResult, error) {
	var turns []Turn
	var conversation []map[string]string

	conversation = append(conversation, map[string]string{
		"role":    "system",
		"content": systemPrompt,
	})

	toolsJSON, _ := json.Marshal(a.tools)
	conversation = append(conversation, map[string]string{
		"role": "user",
		"content": fmt.Sprintf(
			"Available tools:\n%s\n\n%s\n\nStart investigating.",
			toolsJSON, agentInstructions),
	})

	for turnNum := 1; turnNum <= a.cfg.MaxTurns; turnNum++ {
		// Inner retry loop for malformed LLM output. Up to 2 retries
		// per turn — the correction prompt tells the LLM exactly what
		// was wrong and asks it to respond with only the JSON object.
		var response string
		var parsed llmResponse
		var parseOK bool

		for retry := 0; retry <= maxRetriesPerTurn; retry++ {
			turnCtx, cancel := context.WithTimeout(ctx, a.cfg.TurnTimeout)
			var err error
			response, err = a.router.FrontierText(turnCtx,
				agentSystemRole, formatConversation(conversation), 2000)
			cancel()

			if err != nil {
				logging.Error("agent: LLM call failed", "turn", turnNum, "retry", retry, "err", err)
				break
			}

			parsed, parseOK = parseLLMResponse(response)
			if parseOK {
				break
			}

			// Malformed output — send a correction prompt and retry.
			logging.Warn("agent: malformed LLM output, retrying",
				"turn", turnNum, "retry", retry,
				"response_len", len(response),
				"response_preview", truncate(response, 100))
			conversation = append(conversation,
				map[string]string{"role": "assistant", "content": response},
				map[string]string{"role": "user", "content": correctionPrompt})
		}

		if !parseOK {
			// All retries exhausted — count the turn and move on.
			// The conversation already has the correction exchanges
			// appended, so the next turn will see them.
			turns = append(turns, Turn{
				TurnNumber:  turnNum,
				LLMResponse: response,
			})
			logging.Warn("agent: turn produced no valid output after retries", "turn", turnNum)
			continue
		}

		if err := ctx.Err(); err != nil {
			break
		}

		turn := Turn{
			TurnNumber:  turnNum,
			LLMResponse: response,
		}

		if parsed.Done {
			turns = append(turns, turn)
			logging.Info("agent: investigation complete",
				"turns", turnNum,
				"root_cause", parsed.RootCause,
				"confidence", parsed.Confidence)

			return &InvestigationResult{
				RootCause:   parsed.RootCause,
				Confidence:  parsed.Confidence,
				SourceFiles: parsed.FilesToChange,
				Hypotheses:  []string{parsed.RootCause},
				TurnsUsed:   turnNum,
				Trace:       turns,
			}, nil
		}

		if parsed.Tool != "" {
			turn.ToolCall = &ToolCall{Tool: parsed.Tool, Args: parsed.Args}
			result := a.executeTool(ctx, parsed.Tool, parsed.Args)
			turn.ToolResult = &result

			conversation = append(conversation,
				map[string]string{"role": "assistant", "content": response},
				map[string]string{"role": "user", "content": fmt.Sprintf("Tool %s result:\n%s", parsed.Tool, result.Output)},
			)
		} else {
			// Parsed as valid JSON but neither tool nor done — treat
			// as a no-op turn (the LLM may have returned {"thinking": "..."}).
			conversation = append(conversation,
				map[string]string{"role": "assistant", "content": response},
			)
		}

		turns = append(turns, turn)
		logging.Info("agent: turn complete",
			"turn", turnNum,
			"tool", parsed.Tool,
			"done", parsed.Done)

		if turnNum >= a.cfg.CompactThreshold && turnNum%5 == 0 {
			conversation = compactConversation(conversation, systemPrompt)
		}
	}

	logging.Warn("agent: hit turn cap without completion", "max_turns", a.cfg.MaxTurns)

	// Guard against the edge case where the very first LLM call failed
	// (turns is empty). Without this, turns[len(turns)-1] panics with
	// "index out of range [-1]". This can happen when the LLM endpoint
	// is unreachable or the context is cancelled before the first
	// response arrives.
	lastResponse := "(no turns completed — first LLM call failed)"
	turnsUsed := a.cfg.MaxTurns
	if len(turns) > 0 {
		lastResponse = truncate(turns[len(turns)-1].LLMResponse, 200)
	} else {
		// If no turns completed, TurnsUsed should reflect that we
		// didn't actually run the full loop — we broke on the first
		// error. Report 1 so callers know the loop was entered but
		// immediately failed.
		turnsUsed = 1
	}
	return &InvestigationResult{
		RootCause:   fmt.Sprintf("Agent did not complete within %d turns. Last response: %s", a.cfg.MaxTurns, lastResponse),
		Confidence:  0.0,
		SourceFiles: nil,
		TurnsUsed:   turnsUsed,
		Trace:       turns,
	}, nil
}

func (a *Agent) executeTool(ctx context.Context, name string, args map[string]any) ToolResult {
	fn, ok := a.toolFuncs[name]
	if !ok {
		return ToolResult{
			Tool:    name,
			Success: false,
			Output:  fmt.Sprintf("Unknown tool: %s", name),
		}
	}
	return fn(ctx, args)
}

type llmResponse struct {
	Done          bool           `json:"done"`
	Tool          string         `json:"tool"`
	Args          map[string]any `json:"args"`
	RootCause     string         `json:"root_cause"`
	Confidence    float64        `json:"confidence"`
	FilesToChange []string       `json:"files_to_change"`
}

// parseLLMResponse tries to extract a valid tool call or done signal from
// the LLM response. The second return value is false when no valid JSON
// could be extracted — the caller should retry with a correction prompt.
//
// Extraction strategy (in order):
//  1. Direct parse (response is already clean JSON)
//  2. Extract from markdown code block (```json ... ```)
//  3. Extract the first balanced {...} block
//
// The previous lenient fallback (accepting any text containing "done" as a
// completion signal) was removed — it caused the agent to accept garbage
// output as a root cause. Failing explicitly is better than silently
// producing wrong results.
func parseLLMResponse(response string) (llmResponse, bool) {
	var result llmResponse

	// Strategy 1: direct parse.
	if err := json.Unmarshal([]byte(response), &result); err == nil {
		return result, true
	}

	// Strategy 2: extract from markdown code block.
	if code := extractCodeBlock(response); code != "" {
		if err := json.Unmarshal([]byte(code), &result); err == nil {
			return result, true
		}
	}

	// Strategy 3: extract the first balanced JSON object.
	if obj := extractFirstJSONObject(response); obj != "" {
		if err := json.Unmarshal([]byte(obj), &result); err == nil {
			return result, true
		}
	}

	return llmResponse{}, false
}

// extractCodeBlock pulls the content of a ```json or ``` fenced code block
// from the response. Returns "" if no code block is found.
func extractCodeBlock(response string) string {
	// Look for ```json ... ``` or ``` ... ```
	startMarkers := []string{"```json\n", "```json\r\n", "```\n", "```"}
	for _, marker := range startMarkers {
		if idx := strings.Index(response, marker); idx >= 0 {
			rest := response[idx+len(marker):]
			if end := strings.Index(rest, "```"); end >= 0 {
				return strings.TrimSpace(rest[:end])
			}
		}
	}
	return ""
}

// extractFirstJSONObject finds the first balanced {...} in the response.
// It tracks brace depth to handle nested objects correctly. Returns "" if
// no balanced object is found.
func extractFirstJSONObject(response string) string {
	start := strings.Index(response, "{")
	if start < 0 {
		return ""
	}

	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(response); i++ {
		ch := response[i]

		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}

		switch ch {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return response[start : i+1]
			}
		}
	}
	return ""
}

func formatConversation(conv []map[string]string) string {
	var b strings.Builder
	for _, msg := range conv {
		role := msg["role"]
		content := msg["content"]
		if len(content) > 4000 {
			content = content[:4000] + "...(truncated)"
		}
		fmt.Fprintf(&b, "[%s]: %s\n\n", role, content)
	}
	return b.String()
}

func compactConversation(conv []map[string]string, systemPrompt string) []map[string]string {
	if len(conv) <= 4 {
		return conv
	}

	var findings []string
	for i := 2; i < len(conv)-2; i++ {
		msg := conv[i]
		if msg["role"] == "user" && strings.Contains(msg["content"], "Tool ") {
			findings = append(findings, msg["content"])
		}
	}

	summary := fmt.Sprintf("Previous investigation summary (turns 1-%d):\n%s\n\nContinue investigating.", len(conv)-2, strings.Join(findings, "\n"))

	return []map[string]string{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": summary},
		conv[len(conv)-2],
		conv[len(conv)-1],
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// RegisterCodeIntelTools registers the code intelligence tools (find_definition,
// find_callers, find_callees, list_symbols) on the agent.
func (a *Agent) RegisterCodeIntelTools(idx *codeintel.Index) {
	a.RegisterTool("find_definition", "Find the definition of a function, method, class, or type.", `{"name": "symbol name"}`, func(ctx context.Context, args map[string]any) ToolResult {
		name, _ := args["name"].(string)
		if name == "" {
			return ToolResult{Tool: "find_definition", Success: false, Output: "missing 'name' argument"}
		}
		defs := idx.FindDefinition(name)
		if len(defs) == 0 {
			return ToolResult{Tool: "find_definition", Success: false, Output: fmt.Sprintf("No definition found for %q", name)}
		}
		var b strings.Builder
		for _, d := range defs {
			fmt.Fprintf(&b, "%s:%d  %s %s(%s)\n", d.File, d.Line, d.Kind, d.Name, d.Params)
		}
		return ToolResult{Tool: "find_definition", Success: true, Output: b.String()}
	})

	a.RegisterTool("find_callers", "Find all call sites of a function or method.", `{"name": "function name"}`, func(ctx context.Context, args map[string]any) ToolResult {
		name, _ := args["name"].(string)
		if name == "" {
			return ToolResult{Tool: "find_callers", Success: false, Output: "missing 'name' argument"}
		}
		return ToolResult{Tool: "find_callers", Success: true, Output: idx.FormatCallers(name)}
	})

	a.RegisterTool("find_callees", "Find all functions called by a function.", `{"name": "function name"}`, func(ctx context.Context, args map[string]any) ToolResult {
		name, _ := args["name"].(string)
		if name == "" {
			return ToolResult{Tool: "find_callees", Success: false, Output: "missing 'name' argument"}
		}
		callees := idx.FindCallees(name)
		if len(callees) == 0 {
			return ToolResult{Tool: "find_callees", Success: false, Output: fmt.Sprintf("No callees found for %q", name)}
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d callee(s) of %q:\n", len(callees), name)
		for _, c := range callees {
			fmt.Fprintf(&b, "  %s:%d  calls %s\n", c.CallFile, c.CallLine, c.Callee)
		}
		return ToolResult{Tool: "find_callees", Success: true, Output: b.String()}
	})

	a.RegisterTool("list_symbols", "List all symbols (functions, classes, types) in the repo.", `{}`, func(ctx context.Context, args map[string]any) ToolResult {
		return ToolResult{Tool: "list_symbols", Success: true, Output: idx.FormatSymbolList()}
	})
}

// RegisterRepoTools registers file I/O tools (read_file, search_code, list_files).
func (a *Agent) RegisterRepoTools(repoPath string) {
	a.RegisterTool("read_file", "Read a file from the repo. Returns up to 100 lines.", `{"path": "relative/path", "start_line": 1}`, func(ctx context.Context, args map[string]any) ToolResult {
		path, _ := args["path"].(string)
		if path == "" {
			return ToolResult{Tool: "read_file", Success: false, Output: "missing 'path' argument"}
		}
		content, err := repo.ReadFile(repoPath, path)
		if err != nil {
			return ToolResult{Tool: "read_file", Success: false, Output: err.Error()}
		}
		lines := strings.Split(content, "\n")
		startLine := 1
		if sl, ok := args["start_line"].(float64); ok {
			startLine = int(sl)
		}
		if startLine < 1 {
			startLine = 1
		}
		endLine := startLine + MaxFilePreviewLines - 1
		if endLine > len(lines) {
			endLine = len(lines)
		}
		if startLine > len(lines) {
			return ToolResult{Tool: "read_file", Success: false, Output: fmt.Sprintf("start_line %d exceeds file length %d", startLine, len(lines))}
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s (lines %d-%d of %d):\n", path, startLine, endLine, len(lines))
		for i := startLine - 1; i < endLine; i++ {
			fmt.Fprintf(&b, "%4d  %s\n", i+1, lines[i])
		}
		if endLine < len(lines) {
			fmt.Fprintf(&b, "... (%d more lines)\n", len(lines)-endLine)
		}
		return ToolResult{Tool: "read_file", Success: true, Output: b.String()}
	})

	a.RegisterTool("search_code", "Search the repo for a pattern (regex). Returns matches with context.", `{"pattern": "regex pattern", "max_results": 10}`, func(ctx context.Context, args map[string]any) ToolResult {
		pattern, _ := args["pattern"].(string)
		if pattern == "" {
			return ToolResult{Tool: "search_code", Success: false, Output: "missing 'pattern' argument"}
		}
		maxResults := 10
		if mr, ok := args["max_results"].(float64); ok {
			maxResults = int(mr)
		}
		results, err := repo.Search(repoPath, pattern, maxResults)
		if err != nil {
			return ToolResult{Tool: "search_code", Success: false, Output: err.Error()}
		}
		if len(results) == 0 {
			return ToolResult{Tool: "search_code", Success: false, Output: "No matches found."}
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d match(es):\n", len(results))
		for _, r := range results {
			fmt.Fprintf(&b, "  %s:%d  %s\n", r.Path, r.Line, r.Match)
		}
		return ToolResult{Tool: "search_code", Success: true, Output: b.String()}
	})

	a.RegisterTool("list_files", "List files in a directory.", `{"dir": "relative/path"}`, func(ctx context.Context, args map[string]any) ToolResult {
		dir, _ := args["dir"].(string)
		files, err := repo.WalkTree(filepath.Join(repoPath, dir), 100)
		if err != nil {
			return ToolResult{Tool: "list_files", Success: false, Output: err.Error()}
		}
		var b strings.Builder
		for _, f := range files {
			fmt.Fprintf(&b, "  %s\n", f)
		}
		return ToolResult{Tool: "list_files", Success: true, Output: b.String()}
	})
}
