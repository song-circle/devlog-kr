package ollama

// A light engine for local models. Claude Code sends a long system prompt
// and many tools built for Claude; small models drown in it and stop calling
// tools (bug 014). Here the model gets a short instruction and three
// read-only tools, talks to Ollama's own chat API, and a final answer that
// must be JSON is produced with Ollama's schema-constrained output, so even
// a weak model cannot return the wrong shape.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/song-circle/devlog-kr/codebanjang/internal/engine"
)

// Engine runs a local model through Ollama's chat API with its own tools.
type Engine struct {
	MaxTurns int // tool rounds before the model must answer (default 12)
}

func (e *Engine) Name() string { return "ollama" }

const (
	maxFileBytes   = 24000 // per read_file result
	maxListEntries = 300
	maxSearchHits  = 60
)

var tools = []map[string]any{
	tool("read_file", "프로젝트 폴더 안의 텍스트 파일을 읽는다. 긴 파일은 앞부분만 돌려준다.",
		map[string]any{"path": prop("프로젝트 폴더 기준 상대 경로, 예: src/main.go")}, "path"),
	tool("list_files", "프로젝트 폴더 안의 파일 목록을 돌려준다.",
		map[string]any{"dir": prop("상대 경로(비우면 프로젝트 전체)")}),
	tool("search_text", "프로젝트 파일들에서 글자(대소문자 무시)를 찾아 파일:줄과 그 줄을 돌려준다.",
		map[string]any{"text": prop("찾을 글자"), "dir": prop("상대 경로(비우면 전체)")}, "text"),
}

func tool(name, desc string, props map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "function", "function": map[string]any{
		"name": name, "description": desc,
		"parameters": map[string]any{"type": "object", "properties": props, "required": required},
	}}
}

func prop(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

const preamble = "너는 소프트웨어 팀의 직원이다. 프로젝트 폴더의 파일을 도구로 직접 읽어서 확인한 사실로만 일한다.\n" +
	"도구: read_file(path), list_files(dir), search_text(text, dir). 경로는 프로젝트 폴더 기준 상대 경로.\n" +
	"추측하지 말고 먼저 list_files로 어떤 파일이 있는지 보고, 필요한 파일을 read_file로 읽는다. " +
	"충분히 확인했으면 도구를 더 부르지 말고 답을 쓴다. 답은 한국어.\n\n"

type message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	Thinking  string     `json:"thinking,omitempty"` // reasoning models; not sent back
	ToolCalls []toolCall `json:"tool_calls,omitempty"`
	ToolName  string     `json:"tool_name,omitempty"`
}

type toolCall struct {
	Function struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"function"`
}

type chatResponse struct {
	Message         message `json:"message"`
	PromptEvalCount int64   `json:"prompt_eval_count"`
	EvalCount       int64   `json:"eval_count"`
	DoneReason      string  `json:"done_reason"` // "length" = hit the output cap
	Error           string  `json:"error"`
}

// Run implements engine.Engine. Only read-only work is supported.
func (e *Engine) Run(ctx context.Context, spec engine.Spec, onEvent func(engine.Event)) (engine.Result, error) {
	var res engine.Result
	if onEvent == nil {
		onEvent = func(engine.Event) {}
	}
	for _, t := range spec.Tools {
		if t == "Edit" || t == "Write" {
			return res, errors.New("로컬 엔진은 아직 파일을 고칠 수 없습니다(읽기 전용 역할에만 쓰세요)")
		}
	}
	if spec.Tools == nil {
		return res, errors.New("로컬 엔진은 아직 파일을 고칠 수 없습니다(개발자 역할에는 쓸 수 없음)")
	}
	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}
	// The caller holds GPU (orchestrator for local employees, Probe).

	os.MkdirAll(spec.LogDir, 0o755)
	logf, _ := os.Create(filepath.Join(spec.LogDir, "stream.jsonl"))
	if logf != nil {
		defer logf.Close()
	}
	logJSON := func(v any) {
		if logf != nil {
			b, _ := json.Marshal(v)
			logf.Write(append(b, '\n'))
		}
	}

	start := time.Now()
	onEvent(engine.Event{Time: start, Kind: engine.EventInit, Text: spec.Model})
	msgs := []message{
		{Role: "system", Content: preamble + spec.SystemPrompt},
		{Role: "user", Content: spec.Prompt},
	}
	maxTurns := e.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 12
	}
	finish := func(status engine.Status, text string) (engine.Result, error) {
		res.Status, res.Text, res.Duration = status, text, time.Since(start)
		res.Subtype = string(status)
		logJSON(map[string]any{"type": "result", "status": status, "text": text, "turns": res.Turns, "tool_calls": res.ToolCalls})
		return res, nil
	}
	fail := func(err error) (engine.Result, error) {
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return finish(engine.StatusTimeout, err.Error())
		case ctx.Err() != nil:
			return finish(engine.StatusCancelled, err.Error())
		}
		return finish(engine.StatusError, err.Error())
	}

	answer := ""
	for turn := 0; ; turn++ {
		withTools := turn < maxTurns
		if !withTools {
			msgs = append(msgs, message{Role: "user", Content: "도구는 그만 쓰고 지금까지 확인한 내용으로 답을 써라."})
		}
		r, err := chat(ctx, spec.Model, msgs, withTools, nil)
		if err != nil {
			return fail(err)
		}
		res.Turns++
		res.Usage.InputTokens += r.PromptEvalCount
		res.Usage.OutputTokens += r.EvalCount
		logJSON(map[string]any{"type": "assistant", "message": r.Message})
		calls := r.Message.ToolCalls
		if len(calls) == 0 && withTools {
			calls = callsFromText(r.Message.Content) // models that write the call as text
		}
		if len(calls) == 0 || !withTools {
			answer = r.Message.Content
			break
		}
		msgs = append(msgs, message{Role: "assistant", Content: r.Message.Content, ToolCalls: calls})
		for _, c := range calls {
			res.ToolCalls++
			name, args := c.Function.Name, c.Function.Arguments
			out, display, terr := runTool(spec.Dir, name, args)
			onEvent(engine.Event{Time: time.Now(), Kind: engine.EventToolUse, Tool: display, Target: argString(args)})
			if terr != nil {
				res.ToolErrors++
				out = "오류: " + terr.Error()
				onEvent(engine.Event{Time: time.Now(), Kind: engine.EventToolError, Text: out})
			}
			logJSON(map[string]any{"type": "tool", "name": name, "args": args, "result": clip(out, 400)})
			msgs = append(msgs, message{Role: "tool", ToolName: name, Content: out})
		}
	}
	answer = stripThinking(answer)
	if strings.TrimSpace(answer) == "" {
		// An empty answer is a failure, not something for others to review
		// (gpt-oss spent its whole output budget thinking and returned "").
		return fail(errors.New("빈 답(모델이 답을 쓰지 못함)"))
	}
	onEvent(engine.Event{Time: time.Now(), Kind: engine.EventText, Text: answer})

	if spec.JSONSchema != "" {
		// Second step: the same conversation, answer reshaped into the
		// required JSON by Ollama's grammar-constrained output.
		var schema map[string]any
		if err := json.Unmarshal([]byte(spec.JSONSchema), &schema); err != nil {
			return fail(fmt.Errorf("schema: %w", err))
		}
		msgs = append(msgs, message{Role: "assistant", Content: answer},
			message{Role: "user", Content: "위 답을 정해진 JSON 형식으로만 다시 써라. 내용은 바꾸지 말고, 확인한 근거(파일 경로)를 빼지 마라."})
		r, err := chat(ctx, spec.Model, msgs, false, schema)
		if err != nil {
			if ctx.Err() != nil {
				return fail(err)
			}
			// The reshaping step failed or ran away: keep the plain answer;
			// meetings can still read JSON from text or skip this member.
			logJSON(map[string]any{"type": "structured", "error": err.Error()})
			return finish(engine.StatusSuccess, answer)
		}
		res.Turns++
		res.Usage.InputTokens += r.PromptEvalCount
		res.Usage.OutputTokens += r.EvalCount
		js := strings.TrimSpace(stripThinking(r.Message.Content))
		if json.Valid([]byte(js)) {
			res.Structured = []byte(js)
		}
		logJSON(map[string]any{"type": "structured", "json": js})
	}
	return finish(engine.StatusSuccess, answer)
}

// Limits on one model call. Without an output cap a small model can keep
// generating (llama3.1 ran 14 minutes on the JSON step), so every call has
// a token limit and its own time limit.
const (
	maxAnswerTokens = 4096 // reasoning models think inside this budget too
	maxFormatTokens = 1024
	callTimeout     = 5 * time.Minute
	formatTimeout   = 2 * time.Minute
)

// chat makes one model call. If the answer came back empty because the
// output cap was spent on thinking, it asks once more with a larger cap; if
// Ollama's model runner crashed (it restarts on the next request), it waits
// a moment and tries once more.
func chat(ctx context.Context, model string, msgs []message, withTools bool, format map[string]any) (*chatResponse, error) {
	predict := maxAnswerTokens
	if format != nil {
		predict = maxFormatTokens
	}
	r, err := chatOnce(ctx, model, msgs, withTools, format, predict)
	if err != nil && crashed(err) && ctx.Err() == nil {
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			return nil, err
		}
		r, err = chatOnce(ctx, model, msgs, withTools, format, predict)
	}
	if err == nil && format == nil && strings.TrimSpace(r.Message.Content) == "" && len(r.Message.ToolCalls) == 0 && r.DoneReason == "length" {
		r, err = chatOnce(ctx, model, msgs, withTools, format, predict*2)
	}
	return r, err
}

// crashed reports an Ollama runner failure worth one retry.
func crashed(err error) bool {
	s := err.Error()
	return strings.Contains(s, "process has terminated") || strings.Contains(s, "EOF") ||
		strings.Contains(s, "connection reset") || strings.Contains(s, "forcibly closed")
}

func chatOnce(ctx context.Context, model string, msgs []message, withTools bool, format map[string]any, predict int) (*chatResponse, error) {
	opts := map[string]any{"num_ctx": 16384, "num_predict": predict}
	limit := callTimeout
	body := map[string]any{"model": model, "messages": msgs, "stream": false, "options": opts}
	if strings.HasPrefix(model, "gpt-oss") {
		body["think"] = "low" // its reasoning level; "low" leaves room for the answer
	}
	if withTools {
		body["tools"] = tools
	}
	if format != nil {
		body["format"] = format
		limit = formatTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, BaseURL()+"/api/chat", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Ollama에 연결하지 못함: %w", err)
	}
	defer resp.Body.Close()
	var r chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("Ollama 응답을 읽지 못함: %w", err)
	}
	if r.Error != "" {
		return nil, errors.New("Ollama: " + r.Error)
	}
	return &r, nil
}

var thinkRe = regexp.MustCompile(`(?s)<think>.*?</think>`)

// stripThinking removes reasoning blocks some models put in their text.
func stripThinking(s string) string { return strings.TrimSpace(thinkRe.ReplaceAllString(s, "")) }

// callsFromText accepts a tool call written as text, e.g.
// {"name": "read_file", "arguments": {"path": "note.txt"}}.
func callsFromText(text string) []toolCall {
	text = stripThinking(text)
	i := strings.IndexByte(text, '{')
	j := strings.LastIndexByte(text, '}')
	if i < 0 || j <= i {
		return nil
	}
	var raw struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
		Params    map[string]any `json:"parameters"`
	}
	if json.Unmarshal([]byte(text[i:j+1]), &raw) != nil {
		return nil
	}
	switch raw.Name {
	case "read_file", "list_files", "search_text":
	default:
		return nil
	}
	if raw.Arguments == nil {
		raw.Arguments = raw.Params
	}
	var c toolCall
	c.Function.Name, c.Function.Arguments = raw.Name, raw.Arguments
	return []toolCall{c}
}

func argString(args map[string]any) string {
	for _, k := range []string{"path", "text", "dir"} {
		if v, ok := args[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// inside resolves a model-given path within root; nothing outside it.
func inside(root, p string) (string, error) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	p = strings.TrimPrefix(p, "./")
	full := p
	if !filepath.IsAbs(p) {
		full = filepath.Join(root, filepath.FromSlash(p))
	}
	full = filepath.Clean(full)
	rel, err := filepath.Rel(filepath.Clean(root), full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("프로젝트 폴더 밖은 볼 수 없음: %s", p)
	}
	return full, nil
}

var skipDirs = map[string]bool{".git": true, "node_modules": true, "bin": true, "obj": true, "dist": true, "build": true, "Library": true, "Temp": true, ".vs": true}

// runTool executes one read-only tool. display is the name shown in logs.
func runTool(root, name string, args map[string]any) (string, string, error) {
	str := func(k string) string { v, _ := args[k].(string); return v }
	switch name {
	case "read_file":
		full, err := inside(root, str("path"))
		if err != nil {
			return "", "Read", err
		}
		note := ""
		b, err := os.ReadFile(full)
		if err != nil {
			// Small models often give just the file name. If exactly one file
			// in the project has that name, read it; otherwise list the
			// candidates so the next call can get it right.
			found := findByName(root, filepath.Base(filepath.FromSlash(str("path"))))
			switch len(found) {
			case 0:
				return "", "Read", fmt.Errorf("파일이 없음: %s (list_files로 경로를 확인하라)", str("path"))
			case 1:
				if b, err = os.ReadFile(filepath.Join(root, filepath.FromSlash(found[0]))); err != nil {
					return "", "Read", fmt.Errorf("읽을 수 없음: %s", found[0])
				}
				note = fmt.Sprintf("(경로 보정: %s → %s)\n", str("path"), found[0])
			default:
				if len(found) > 8 {
					found = found[:8]
				}
				return "", "Read", fmt.Errorf("같은 이름의 파일이 여러 개: %s — 이 중 하나의 전체 경로로 다시 읽어라", strings.Join(found, ", "))
			}
		}
		if !utf8ish(b) {
			return "", "Read", fmt.Errorf("텍스트 파일이 아님: %s", str("path"))
		}
		return note + numbered(clip(string(b), maxFileBytes)), "Read", nil
	case "list_files":
		dir, err := inside(root, str("dir"))
		if err != nil {
			return "", "Glob", err
		}
		var out []string
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			if !d.IsDir() && len(out) < maxListEntries {
				rel, _ := filepath.Rel(root, p)
				out = append(out, filepath.ToSlash(rel))
			}
			return nil
		})
		sort.Strings(out)
		if len(out) == maxListEntries {
			out = append(out, fmt.Sprintf("... (%d개까지만 표시)", maxListEntries))
		}
		return strings.Join(out, "\n"), "Glob", nil
	case "search_text":
		q := strings.ToLower(strings.TrimSpace(str("text")))
		if q == "" {
			return "", "Grep", errors.New("찾을 글자가 비어 있음")
		}
		dir, err := inside(root, str("dir"))
		if err != nil {
			return "", "Grep", err
		}
		var hits []string
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || len(hits) >= maxSearchHits {
				return nil
			}
			if d.IsDir() {
				if skipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			f, err := os.Open(p)
			if err != nil {
				return nil
			}
			defer f.Close()
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
			for n := 1; sc.Scan() && len(hits) < maxSearchHits; n++ {
				if strings.Contains(strings.ToLower(sc.Text()), q) {
					rel, _ := filepath.Rel(root, p)
					hits = append(hits, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(rel), n, clip(strings.TrimSpace(sc.Text()), 200)))
				}
			}
			return nil
		})
		if len(hits) == 0 {
			return "찾지 못함", "Grep", nil
		}
		return strings.Join(hits, "\n"), "Grep", nil
	}
	return "", name, fmt.Errorf("없는 도구: %s (read_file, list_files, search_text만 있음)", name)
}

// findByName lists project files (relative, slash-separated) whose name
// matches, ignoring case.
func findByName(root, name string) []string {
	var out []string
	if name == "" || name == "." {
		return nil
	}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && skipDirs[d.Name()] {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.EqualFold(d.Name(), name) {
			rel, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n... (잘림)"
}

func numbered(s string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = fmt.Sprintf("%4d  %s", i+1, lines[i])
	}
	return strings.Join(lines, "\n")
}

func utf8ish(b []byte) bool {
	if len(b) > 8000 {
		b = b[:8000]
	}
	return !bytes.Contains(b, []byte{0})
}
