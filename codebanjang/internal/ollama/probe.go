package ollama

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/song-circle/devlog-kr/codebanjang/internal/engine"
	"github.com/song-circle/devlog-kr/codebanjang/internal/engine/claudecode"
)

// ProbeResult says whether a local model can work as an employee: read a
// file through a tool call and answer in the required JSON format.
type ProbeResult struct {
	Model     string  `json:"model"`
	ToolCalls int     `json:"toolCalls"` // tool calls actually made
	ReadFile  bool    `json:"readFile"`  // it found the secret word, so it read the file
	JSON      bool    `json:"json"`      // the answer came back in the required JSON
	Seconds   float64 `json:"seconds"`
	Note      string  `json:"note,omitempty"`
	At        string  `json:"at"`
}

// OK means the model can take part in meetings and reviews.
func (p ProbeResult) OK() bool { return p.ReadFile && p.JSON }

const secret = "귤나무-4821"

// Probe runs the test in a temporary folder. profileDir is an empty Claude
// Code profile used for local runs.
func Probe(ctx context.Context, model, profileDir string, timeout time.Duration) ProbeResult {
	return probe(ctx, model, profileDir, timeout, &Engine{})
}

// ProbeClaudeCode runs the same test inside Claude Code (for comparison).
func ProbeClaudeCode(ctx context.Context, model, profileDir string, timeout time.Duration) ProbeResult {
	return probe(ctx, model, profileDir, timeout, &claudecode.Engine{})
}

func probe(ctx context.Context, model, profileDir string, timeout time.Duration, eng engine.Engine) ProbeResult {
	GPU.Lock()
	defer GPU.Unlock()
	res := ProbeResult{Model: model, At: time.Now().Format(time.RFC3339)}
	dir, err := os.MkdirTemp("", "aio-probe-")
	if err != nil {
		res.Note = err.Error()
		return res
	}
	defer os.RemoveAll(dir)
	os.WriteFile(filepath.Join(dir, "note.txt"), []byte("이 파일의 비밀 단어: "+secret+"\n"), 0o644)
	os.MkdirAll(profileDir, 0o755)

	out, err := eng.Run(ctx, engine.Spec{
		Prompt:     "note.txt 파일을 도구로 직접 읽고, 그 안의 비밀 단어를 word에 넣어 답해.",
		Dir:        dir,
		Model:      model,
		Tools:      []string{"Read", "Glob", "Grep"},
		Timeout:    timeout,
		LogDir:     filepath.Join(dir, "log"),
		JSONSchema: `{"type":"object","properties":{"word":{"type":"string"}},"required":["word"]}`,
		ProfileDir: profileDir,
		Env:        Env(model),
	}, nil)
	res.Seconds = out.Duration.Seconds()
	res.ToolCalls = out.ToolCalls
	if err != nil {
		res.Note = err.Error()
		return res
	}
	if out.Status != engine.StatusSuccess {
		res.Note = string(out.Status) + " " + strings.TrimSpace(out.Text)
	}
	var w struct {
		Word string `json:"word"`
	}
	if len(out.Structured) > 0 && json.Unmarshal(out.Structured, &w) == nil {
		res.JSON = true
	} else if i, j := strings.IndexByte(out.Text, '{'), strings.LastIndexByte(out.Text, '}'); i >= 0 && j > i && json.Unmarshal([]byte(out.Text[i:j+1]), &w) == nil {
		res.JSON = true // JSON written in the text: accepted by meetings too
	}
	res.ReadFile = strings.Contains(w.Word, "4821") || strings.Contains(out.Text, "4821")
	if res.ToolCalls == 0 {
		res.ReadFile = false // knowing the word without reading is impossible; be strict anyway
		if res.Note == "" {
			res.Note = "도구를 한 번도 호출하지 않음"
		}
	} else if !res.ReadFile && res.Note == "" {
		res.Note = "도구는 불렀지만 파일 내용을 가져오지 못함"
	}
	return res
}
