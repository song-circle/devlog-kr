// Package engine defines the interface every coding-agent engine implements.
// An engine runs one task inside a working directory and reports what it did
// as a stream of events plus a final result.
package engine

import (
	"context"
	"time"
)

// Spec describes one engine run.
type Spec struct {
	Prompt       string        // task text, sent on stdin
	Dir          string        // working directory (a git worktree)
	Model        string        // engine-specific model name; empty = engine default
	Tools        []string      // tools the agent may use; engine-specific names
	MaxBudgetUSD float64       // 0 = no limit passed to the engine
	Timeout      time.Duration // 0 = no timeout
	LogDir       string        // raw output is written here
	SystemPrompt string        // role instructions appended to the engine's own
	JSONSchema   string        // if set, the final answer must match this schema
	ProfileDir   string        // engine account folder (own login); empty = the PC's default login
	Env          []string      // extra environment, e.g. pointing the engine at a local model server
}

// EventKind tells what an Event carries.
type EventKind string

const (
	EventInit      EventKind = "init"       // session started (Text = model)
	EventText      EventKind = "text"       // assistant text
	EventToolUse   EventKind = "tool_use"   // agent called a tool (Tool, Target)
	EventToolError EventKind = "tool_error" // a tool call failed or was denied
)

// Event is one thing that happened during a run.
type Event struct {
	Time   time.Time
	Kind   EventKind
	Text   string
	Tool   string
	Target string // file path, pattern or command the tool acted on
}

// Usage is token and cost accounting for a run.
type Usage struct {
	InputTokens       int64
	OutputTokens      int64
	CacheReadTokens   int64
	CacheCreateTokens int64
	CostUSD           float64 // as reported by the engine (list-price estimate)
	// CostEstimated is true when the engine ended without its own totals
	// (killed, crashed) and the numbers were summed from per-message usage
	// and priced locally. Side calls the engine makes internally are missing.
	CostEstimated  bool
	UnpricedModels []string // models seen without a known price (cost excludes them)
}

// Result is the outcome of a run.
type Result struct {
	Status      Status
	Subtype     string // engine's own end reason
	Text        string // final assistant message
	Structured  []byte // final answer as JSON when Spec.JSONSchema was set
	Turns       int
	Duration    time.Duration
	Usage       Usage
	Denials     int // permission denials reported by the engine
	ExitCode    int
	SessionID   string
	ToolCalls   int
	ToolErrors  int
	StderrBytes int64
}

// Status is the normalized outcome.
type Status string

const (
	StatusSuccess   Status = "success"
	StatusError     Status = "error"     // engine reported an error result
	StatusCrashed   Status = "crashed"   // process ended without a result
	StatusTimeout   Status = "timeout"   // killed by Spec.Timeout
	StatusCancelled Status = "cancelled" // killed because ctx was cancelled
	StatusLimited   Status = "limited"   // the account's usage limit was reached (HTTP 429); Text says when it resets
)

// Engine runs a Spec. onEvent is called from the Run goroutine, in order.
type Engine interface {
	Name() string
	Run(ctx context.Context, spec Spec, onEvent func(Event)) (Result, error)
}
