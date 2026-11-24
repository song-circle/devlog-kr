// aio-ollama tests every model installed in Ollama for employee work (read a
// file through a tool call, answer in JSON) and saves the results where the
// app shows them.
//
//	aio-ollama            test all installed models
//	aio-ollama qwen3:8b   test one model
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/song-circle/devlog-kr/codebanjang/internal/appdir"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"github.com/song-circle/devlog-kr/codebanjang/internal/ollama"
	"github.com/song-circle/devlog-kr/codebanjang/internal/store"
)

func main() {
	timeout := flag.Duration("timeout", 5*time.Minute, "max time per model")
	cc := flag.Bool("claude-code", false, "test inside Claude Code instead of the light local engine (not saved)")
	flag.Parse()
	windows.SetConsoleOutputCP(65001)
	ctx := context.Background()
	st := ollama.Check(ctx)
	if !st.Running {
		fmt.Fprintln(os.Stderr, st.Error)
		os.Exit(1)
	}
	models := flag.Args()
	if len(models) == 0 {
		for _, m := range st.Models {
			models = append(models, m.Name)
		}
	}
	data := appdir.Dir()
	db, err := store.Open(filepath.Join(data, "aio.db"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer db.Close()
	saved := map[string]ollama.ProbeResult{}
	if v, _ := db.Setting("ollama_probe"); v != "" {
		json.Unmarshal([]byte(v), &saved)
	}
	fmt.Printf("%-24s %-6s %-6s %-6s %7s  %s\n", "model", "tools", "read", "json", "sec", "note")
	for _, m := range models {
		profile := filepath.Join(data, "profiles", "probe-"+strings.NewReplacer(":", "_", "/", "_").Replace(m))
		var r ollama.ProbeResult
		if *cc {
			r = ollama.ProbeClaudeCode(ctx, m, profile, *timeout)
		} else {
			r = ollama.Probe(ctx, m, profile, *timeout)
			saved[m] = r
		}
		ok := "NO"
		if r.OK() {
			ok = "OK"
		}
		fmt.Printf("%-24s %-6d %-6v %-6v %7.0f  %s %s\n", m, r.ToolCalls, r.ReadFile, r.JSON, r.Seconds, ok, r.Note)
		if !*cc {
			b, _ := json.Marshal(saved)
			db.SetSetting("ollama_probe", string(b))
		}
	}
}
