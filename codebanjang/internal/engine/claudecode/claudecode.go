// Package claudecode runs Claude Code non-interactively (claude -p) and
// reads its stream-json output.
package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/song-circle/devlog-kr/codebanjang/internal/engine"
	"github.com/song-circle/devlog-kr/codebanjang/internal/proc"
)

// Engine runs the claude CLI.
type Engine struct {
	Exe string // path to claude.exe; empty = find it
}

func (e *Engine) Name() string { return "claude-code" }

// FindExe locates claude.exe. The npm install puts a claude.cmd shim on PATH;
// running the .cmd would go through cmd.exe and its quoting rules, so we
// resolve the real exe behind it.
func FindExe() (string, error) {
	p, err := exec.LookPath("claude")
	if err != nil {
		return "", err
	}
	if strings.EqualFold(filepath.Ext(p), ".exe") {
		return p, nil
	}
	exe := filepath.Join(filepath.Dir(p), "node_modules", "@anthropic-ai", "claude-code", "bin", "claude.exe")
	if _, err := os.Stat(exe); err == nil {
		return exe, nil
	}
	return "", fmt.Errorf("found %s but not the claude.exe behind it", p)
}

// Args builds the command line. Kept separate so tests can check it.
func Args(spec engine.Spec) []string {
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose", // required for stream-json with -p
		// Per-response usage, so a killed run still has token/cost numbers.
		"--include-partial-messages",
		// Edits are applied without asking; anything else that would ask is denied.
		"--permission-mode", "acceptEdits",
		"--permission-prompts", "none",
		// No shell tools unless listed, file tools confined to the working dir.
		"--restricted",
		// Ignore the user's own CLAUDE.md, hooks, plugins and MCP servers so runs
		// are reproducible and nothing from the host setup leaks in.
		"--safe-mode",
		"--no-session-persistence",
	}
	tools := spec.Tools
	if len(tools) == 0 {
		tools = []string{"Read", "Edit", "Write", "Glob", "Grep"}
	}
	args = append(args, "--tools", strings.Join(tools, ","))
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", spec.SystemPrompt)
	}
	if spec.JSONSchema != "" {
		args = append(args, "--json-schema", spec.JSONSchema)
	}
	if spec.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", fmt.Sprintf("%.2f", spec.MaxBudgetUSD))
	}
	return args
}

// Run starts claude in spec.Dir, feeds the prompt on stdin and parses events.
func (e *Engine) Run(ctx context.Context, spec engine.Spec, onEvent func(engine.Event)) (engine.Result, error) {
	var res engine.Result
	exe := e.Exe
	if exe == "" {
		var err error
		if exe, err = FindExe(); err != nil {
			return res, err
		}
	}
	if err := os.MkdirAll(spec.LogDir, 0o755); err != nil {
		return res, err
	}
	rawLog, err := os.Create(filepath.Join(spec.LogDir, "stream.jsonl"))
	if err != nil {
		return res, err
	}
	defer rawLog.Close()
	errLog, err := os.Create(filepath.Join(spec.LogDir, "stderr.txt"))
	if err != nil {
		return res, err
	}
	defer errLog.Close()

	cmd := exec.Command(exe, Args(spec)...)
	cmd.Dir = spec.Dir
	cmd.Stdin = strings.NewReader(spec.Prompt)
	if spec.ProfileDir != "" {
		cmd.Env = ProfileEnv(spec.ProfileDir)
	}
	if len(spec.Env) > 0 {
		if cmd.Env == nil {
			cmd.Env = os.Environ()
		}
		cmd.Env = append(cmd.Env, spec.Env...)
	}
	cmd.Stderr = errLog
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return res, err
	}

	job, err := proc.NewJob(0)
	if err != nil {
		return res, err
	}
	defer job.Close()

	start := time.Now()
	if err := job.Start(cmd); err != nil {
		return res, fmt.Errorf("start claude: %w", err)
	}

	// Kill the whole process tree on timeout or cancel.
	runCtx := ctx
	var cancelTimeout context.CancelFunc = func() {}
	if spec.Timeout > 0 {
		runCtx, cancelTimeout = context.WithTimeout(ctx, spec.Timeout)
	}
	defer cancelTimeout()
	done := make(chan struct{})
	go func() {
		select {
		case <-runCtx.Done():
			job.Kill()
		case <-done:
		}
	}()

	gotResult, parseErr := parseStream(io.TeeReader(stdout, rawLog), onEvent, &res)
	waitErr := cmd.Wait()
	close(done)
	res.Duration = time.Since(start)
	if st, ok := errLog.Stat(); ok == nil {
		res.StderrBytes = st.Size()
	}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}

	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil:
		res.Status = engine.StatusTimeout
	case ctx.Err() != nil:
		res.Status = engine.StatusCancelled
	case !gotResult:
		res.Status = engine.StatusCrashed
	}
	if parseErr != nil {
		return res, parseErr
	}
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) {
		return res, waitErr
	}
	return res, nil
}

// --- stream-json parsing ---

type streamMsg struct {
	Type    string       `json:"type"`
	Subtype string       `json:"subtype"`
	Model   string       `json:"model"` // system/init
	Event   *streamEvent `json:"event"` // stream_event
	Message *struct {
		Content []contentBlock `json:"content"`
	} `json:"message"`
	// result
	IsError           bool            `json:"is_error"`
	Result            string          `json:"result"`
	NumTurns          int             `json:"num_turns"`
	TotalCostUSD      float64         `json:"total_cost_usd"`
	SessionID         string          `json:"session_id"`
	PermissionDenials json.RawMessage `json:"permission_denials"`
	StructuredOutput  json.RawMessage `json:"structured_output"`
	APIErrorStatus    int             `json:"api_error_status"`
	Usage             *struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

// streamEvent is a raw API stream event (only with --include-partial-messages).
// message_start carries the input/cache usage of a response, message_delta its
// final output tokens. The assistant messages above only carry a usage snapshot
// taken when output started, so output tokens there are far too low.
type streamEvent struct {
	Type    string `json:"type"`
	Message *struct {
		ID    string    `json:"id"`
		Model string    `json:"model"`
		Usage *apiUsage `json:"usage"`
	} `json:"message"`
	Usage *apiUsage `json:"usage"`
}

type apiUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheCreation            *struct {
		Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

// usageTracker sums per-response usage so a run that never reaches its
// result message still has token and cost numbers.
type usageTracker struct {
	msgs []engine.MessageUsage
}

func (t *usageTracker) start(model string, u *apiUsage) {
	m := engine.MessageUsage{Model: model}
	if u != nil {
		m.Input = u.InputTokens
		m.Output = u.OutputTokens
		m.CacheRead = u.CacheReadInputTokens
		if c := u.CacheCreation; c != nil {
			m.CacheWrite5m, m.CacheWrite1h = c.Ephemeral5m, c.Ephemeral1h
		} else {
			m.CacheWrite5m = u.CacheCreationInputTokens
		}
	}
	t.msgs = append(t.msgs, m)
}

func (t *usageTracker) delta(u *apiUsage) {
	if u == nil || len(t.msgs) == 0 {
		return
	}
	// message_delta usage is cumulative for the current response.
	if u.OutputTokens > 0 {
		t.msgs[len(t.msgs)-1].Output = u.OutputTokens
	}
}

// fill writes the summed usage into res as a local estimate.
func (t *usageTracker) fill(res *engine.Result) {
	var u engine.Usage
	unpriced := map[string]bool{}
	for _, m := range t.msgs {
		u.InputTokens += m.Input
		u.OutputTokens += m.Output
		u.CacheReadTokens += m.CacheRead
		u.CacheCreateTokens += m.CacheWrite5m + m.CacheWrite1h
		if c, ok := m.Cost(); ok {
			u.CostUSD += c
		} else if !unpriced[m.Model] {
			unpriced[m.Model] = true
			u.UnpricedModels = append(u.UnpricedModels, m.Model)
		}
	}
	u.CostEstimated = true
	res.Usage = u
}

type contentBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	IsError bool            `json:"is_error"`
	Content json.RawMessage `json:"content"`
}

// parseStream reads stream-json lines until EOF. It returns whether a result
// message was seen. Lines that are not JSON are ignored (kept in the raw log).
func parseStream(r io.Reader, onEvent func(engine.Event), res *engine.Result) (bool, error) {
	if onEvent == nil {
		onEvent = func(engine.Event) {}
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20) // tool results can be large
	gotResult := false
	var usage usageTracker
	defer func() {
		if !gotResult {
			usage.fill(res)
		}
	}()
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var m streamMsg
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		now := time.Now()
		switch m.Type {
		case "stream_event":
			if e := m.Event; e != nil {
				switch e.Type {
				case "message_start":
					if e.Message != nil {
						usage.start(e.Message.Model, e.Message.Usage)
					}
				case "message_delta":
					usage.delta(e.Usage)
				}
			}
		case "system":
			if m.Subtype == "init" {
				onEvent(engine.Event{Time: now, Kind: engine.EventInit, Text: m.Model})
			}
		case "assistant":
			if m.Message == nil {
				continue
			}
			for _, b := range m.Message.Content {
				switch b.Type {
				case "text":
					if strings.TrimSpace(b.Text) != "" {
						onEvent(engine.Event{Time: now, Kind: engine.EventText, Text: b.Text})
					}
				case "tool_use":
					res.ToolCalls++
					onEvent(engine.Event{Time: now, Kind: engine.EventToolUse, Tool: b.Name, Target: toolTarget(b.Input)})
				}
			}
		case "user":
			if m.Message == nil {
				continue
			}
			for _, b := range m.Message.Content {
				if b.Type == "tool_result" && b.IsError {
					res.ToolErrors++
					onEvent(engine.Event{Time: now, Kind: engine.EventToolError, Text: toolResultText(b.Content)})
				}
			}
		case "result":
			gotResult = true
			res.Subtype = m.Subtype
			res.Text = m.Result
			if len(m.StructuredOutput) > 0 && string(m.StructuredOutput) != "null" {
				res.Structured = append([]byte(nil), m.StructuredOutput...)
			}
			res.Turns = m.NumTurns
			res.SessionID = m.SessionID
			res.Usage.CostUSD = m.TotalCostUSD
			if m.Usage != nil {
				res.Usage.InputTokens = m.Usage.InputTokens
				res.Usage.OutputTokens = m.Usage.OutputTokens
				res.Usage.CacheReadTokens = m.Usage.CacheReadInputTokens
				res.Usage.CacheCreateTokens = m.Usage.CacheCreationInputTokens
			}
			var denials []json.RawMessage
			if json.Unmarshal(m.PermissionDenials, &denials) == nil {
				res.Denials = len(denials)
			}
			if m.IsError && m.APIErrorStatus == 429 {
				res.Status = engine.StatusLimited // e.g. "You've hit your session limit · resets 1:50pm"
			} else if m.IsError {
				res.Status = engine.StatusError
			} else {
				res.Status = engine.StatusSuccess
			}
		}
	}
	return gotResult, sc.Err()
}

// toolTarget pulls the most useful field out of a tool input for display.
func toolTarget(input json.RawMessage) string {
	var in map[string]any
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	for _, k := range []string{"file_path", "path", "pattern", "command"} {
		if v, ok := in[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// toolResultText flattens a tool_result content (string or block list).
func toolResultText(c json.RawMessage) string {
	var s string
	if json.Unmarshal(c, &s) == nil {
		return s
	}
	var blocks []contentBlock
	if json.Unmarshal(c, &blocks) == nil {
		var sb strings.Builder
		for _, b := range blocks {
			sb.WriteString(b.Text)
		}
		return sb.String()
	}
	return string(c)
}
