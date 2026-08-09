// Package agent implements the investigation agent loop — a single-threaded,
// continuous-context LLM agent that uses tools to investigate and fix bugs.
//
// The agent runs for up to MaxTurns iterations. Each turn:
//  1. The LLM receives the conversation history + available tools.
//  2. The LLM emits a tool call (one per turn).
//  3. The tool is executed locally (file read, search, code intel, etc.).
//  4. The result is appended to the conversation history.
//  5. If the LLM emits "done", the loop exits with the root cause + fix.
//
// Context management: after TurnCompactThreshold turns, earlier turns are
// summarized into a compact "key findings" block to prevent context bloat.
package agent
