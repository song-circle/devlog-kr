package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/song-circle/devlog-kr/codebanjang/internal/engine"
)

// fakeOllama answers /api/chat from a script: first a tool call (given as
// text when asText is set, like qwen2.5-coder does), then a plain answer,
// then the JSON asked for with "format".
func fakeOllama(t *testing.T, asText bool, seen *[]map[string]any) *httptest.Server {
	step := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		*seen = append(*seen, body)
		msg := map[string]any{"role": "assistant", "content": ""}
		switch {
		case body["format"] != nil:
			msg["content"] = `{"word":"귤나무-4821"}`
		case step == 0:
			call := map[string]any{"function": map[string]any{"name": "read_file", "arguments": map[string]any{"path": "note.txt"}}}
			if asText {
				msg["content"] = `{"name": "read_file", "arguments": {"path": "note.txt"}}`
			} else {
				msg["tool_calls"] = []any{call}
			}
		default:
			msg["content"] = "<think>음</think>비밀 단어는 귤나무-4821"
		}
		step++
		json.NewEncoder(w).Encode(map[string]any{"message": msg, "prompt_eval_count": 10, "eval_count": 5})
	}))
}

func runFake(t *testing.T, asText bool) (engine.Result, []map[string]any, string) {
	var seen []map[string]any
	srv := fakeOllama(t, asText, &seen)
	defer srv.Close()
	t.Setenv("OLLAMA_HOST", srv.URL)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "note.txt"), []byte("비밀 단어: 귤나무-4821\n"), 0o644)
	var events []engine.Event
	res, err := (&Engine{}).Run(context.Background(), engine.Spec{
		Prompt: "읽어", Dir: dir, Model: "m", Tools: []string{"Read", "Glob", "Grep"}, LogDir: filepath.Join(dir, "log"),
		JSONSchema: `{"type":"object","properties":{"word":{"type":"string"}},"required":["word"]}`,
	}, func(e engine.Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	return res, seen, dir
}

func TestEngineToolLoopAndFormat(t *testing.T) {
	for _, asText := range []bool{false, true} {
		res, seen, _ := runFake(t, asText)
		if res.Status != engine.StatusSuccess || res.ToolCalls != 1 || res.Text != "비밀 단어는 귤나무-4821" {
			t.Errorf("asText=%v: %+v", asText, res)
		}
		if string(res.Structured) != `{"word":"귤나무-4821"}` {
			t.Errorf("asText=%v structured %s", asText, res.Structured)
		}
		// The tool result went back to the model with the file's text.
		msgs, _ := seen[1]["messages"].([]any)
		last, _ := msgs[len(msgs)-1].(map[string]any)
		if last["role"] != "tool" || !strings.Contains(last["content"].(string), "귤나무-4821") {
			t.Errorf("asText=%v tool message %+v", asText, last)
		}
		if seen[len(seen)-1]["format"] == nil || seen[len(seen)-1]["tools"] != nil {
			t.Errorf("asText=%v last request should use format without tools", asText)
		}
	}
}

func TestToolsStayInsideProject(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\nworld\n"), 0o644)
	if _, _, err := runTool(root, "read_file", map[string]any{"path": "../../secret.txt"}); err == nil {
		t.Error("read outside the project allowed")
	}
	if _, _, err := runTool(root, "read_file", map[string]any{"path": `C:\Windows\win.ini`}); err == nil {
		t.Error("absolute path outside the project allowed")
	}
	out, _, err := runTool(root, "search_text", map[string]any{"text": "WORLD"})
	if err != nil || !strings.Contains(out, "a.txt:2:") {
		t.Errorf("search: %q %v", out, err)
	}
	out, _, _ = runTool(root, "list_files", map[string]any{})
	if strings.TrimSpace(out) != "a.txt" {
		t.Errorf("list: %q", out)
	}
}

func TestEngineRefusesWriting(t *testing.T) {
	if _, err := (&Engine{}).Run(context.Background(), engine.Spec{Tools: nil}, nil); err == nil {
		t.Error("developer (write) work accepted")
	}
}

// A bare file name is found inside the project when it is unique (bug 014:
// llama3.1 kept asking for "GameManager.cs" that lives in a subfolder).
func TestReadFileByNameOnly(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "Game", "Scripts"), 0o755)
	os.MkdirAll(filepath.Join(root, "Tools"), 0o755)
	os.WriteFile(filepath.Join(root, "Game", "Scripts", "GameManager.cs"), []byte("class GameManager {}\n"), 0o644)
	out, _, err := runTool(root, "read_file", map[string]any{"path": "GameManager.cs"})
	if err != nil || !strings.Contains(out, "경로 보정") || !strings.Contains(out, "class GameManager") {
		t.Errorf("unique name: %q %v", out, err)
	}
	os.WriteFile(filepath.Join(root, "Tools", "GameManager.cs"), []byte("x\n"), 0o644)
	if _, _, err := runTool(root, "read_file", map[string]any{"path": "GameManager.cs"}); err == nil || !strings.Contains(err.Error(), "Tools/GameManager.cs") {
		t.Errorf("two candidates should be listed: %v", err)
	}
	if _, _, err := runTool(root, "read_file", map[string]any{"path": "Nope.cs"}); err == nil {
		t.Error("missing file accepted")
	}
}

// A reasoning model that spends the whole budget thinking returns "": the
// engine asks once more with a larger budget, and an answer that stays empty
// is a failure (bug 014: gpt-oss handed an empty draft to the reviewers).
func TestEmptyAnswerRetriedThenFails(t *testing.T) {
	for _, recovers := range []bool{true, false} {
		var predicts []float64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			predicts = append(predicts, body["options"].(map[string]any)["num_predict"].(float64))
			msg := map[string]any{"role": "assistant", "content": "", "thinking": "음..."}
			if recovers && len(predicts) == 2 {
				msg["content"] = "다 생각했다"
			}
			json.NewEncoder(w).Encode(map[string]any{"message": msg, "done_reason": "length"})
		}))
		t.Setenv("OLLAMA_HOST", srv.URL)
		res, _ := (&Engine{MaxTurns: 1}).Run(context.Background(), engine.Spec{Prompt: "x", Dir: t.TempDir(), Model: "gpt-oss:20b",
			Tools: []string{"Read"}, LogDir: filepath.Join(t.TempDir(), "log")}, nil)
		srv.Close()
		if len(predicts) < 2 || predicts[1] != 2*predicts[0] {
			t.Errorf("recovers=%v: num_predict sequence %v", recovers, predicts)
		}
		if recovers && (res.Status != engine.StatusSuccess || res.Text != "다 생각했다") {
			t.Errorf("recovered: %+v", res)
		}
		if !recovers && res.Status != engine.StatusError {
			t.Errorf("still empty should fail: %+v", res)
		}
	}
}
