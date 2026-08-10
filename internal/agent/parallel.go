package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// ParallelToolResult holds the results of executing multiple tools in parallel.
type ParallelToolResult struct {
	Results []ToolResult
}

// executeToolsParallel runs multiple tool calls concurrently and returns
// all results. Each tool runs in its own goroutine. If a tool panics,
// the goroutine recovers and returns an error result.
//
// Based on OpenHands "Parallel Tool Execution" pattern — the agent can
// call read_file on 3 different files in one turn instead of 3 separate
// turns, reducing the number of LLM calls by 3x.
func (a *Agent) executeToolsParallel(ctx context.Context, calls []ToolCall) []ToolResult {
	results := make([]ToolResult, len(calls))
	var wg sync.WaitGroup

	for i, call := range calls {
		wg.Add(1)
		go func(idx int, c ToolCall) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					results[idx] = ToolResult{
						Tool:    c.Tool,
						Success: false,
						Output:  fmt.Sprintf("tool panicked: %v", r),
					}
				}
			}()

			results[idx] = a.executeTool(ctx, c.Tool, c.Args)
		}(i, call)
	}

	wg.Wait()
	return results
}

// formatParallelResults formats multiple tool results into a single
// conversation message.
func formatParallelResults(results []ToolResult) string {
	var b strings.Builder
	for i, r := range results {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "Tool %s result:\n%s", r.Tool, r.Output)
	}
	return b.String()
}
