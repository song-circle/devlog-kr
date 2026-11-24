// aio-run sends one request through the team (PM → developer → checks →
// fixes) in an isolated worktree, records everything in SQLite, commits the
// result on its own branch and prints a report. Nothing is merged: applying
// the branch is the user's decision.
//
//	aio-run -repo C:\path\to\repo -check "go test ./..." -request "..."
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/song-circle/devlog-kr/codebanjang/internal/appdir"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"github.com/song-circle/devlog-kr/codebanjang/internal/engine/claudecode"
	"github.com/song-circle/devlog-kr/codebanjang/internal/ollama"
	"github.com/song-circle/devlog-kr/codebanjang/internal/orchestrator"
	"github.com/song-circle/devlog-kr/codebanjang/internal/report"
	"github.com/song-circle/devlog-kr/codebanjang/internal/store"
)

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, "; ") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func main() { os.Exit(run()) }

func run() int {
	var checks, protect multiFlag
	var (
		repo        = flag.String("repo", "", "git repository to work on (required)")
		request     = flag.String("request", "", "what you want done")
		requestFile = flag.String("request-file", "", "read the request from a UTF-8 file")
		pmModel     = flag.String("pm-model", "sonnet", "model for the PM")
		devModel    = flag.String("dev-model", "sonnet", "model for the developer")
		fixRounds   = flag.Int("fix-rounds", 3, "max fix rounds after failed checks")
		budget      = flag.Float64("budget", 2.0, "max USD for the whole request")
		taskTO      = flag.Duration("task-timeout", 10*time.Minute, "max time per agent run")
		checkTO     = flag.Duration("check-timeout", 5*time.Minute, "max time per check")
		solo        = flag.Bool("solo", false, "no PM: the developer gets the request as one task")
		reviewFlag  = flag.Bool("review", true, "after checks pass, a QA agent reviews the diff (team mode)")
		qaModel     = flag.String("qa-model", "", "model for the QA reviewer (default: dev model)")
		mode        = flag.String("mode", "", "ask / plan / audit (read only) or solo / team / review; overrides -solo and -review")
		panel       = flag.String("review-panel", "", "cross-review panel, e.g. \"QA1:sonnet,QA2:opus,Local:qwen3:8b@ollama\" (review stage needs 2+, audit 1+)")
		notes       = flag.String("notes", "", "project notes given to every employee")
		dataDir     = flag.String("data", defaultDataDir(), "worktrees, logs and database")
	)
	flag.Var(&checks, "check", "check command run in the worktree (repeatable); all must pass")
	flag.Var(&protect, "protect", "glob of files the agents may not change (repeatable), e.g. *_test.go")
	flag.Parse()
	windows.SetConsoleOutputCP(65001)

	if *repo == "" || (*request == "") == (*requestFile == "") {
		fmt.Fprintln(os.Stderr, "usage: aio-run -repo DIR (-request TEXT | -request-file FILE) [-check CMD]... [-protect GLOB]... [options]")
		flag.PrintDefaults()
		return 2
	}
	text := *request
	if *requestFile != "" {
		b, err := os.ReadFile(*requestFile)
		if err != nil {
			return fail(err)
		}
		text = string(b)
	}
	if len(checks) == 0 {
		fmt.Println("note: no -check given; the team's work will not be verified")
	}

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		return fail(err)
	}
	st, err := store.Open(filepath.Join(*dataDir, "aio.db"))
	if err != nil {
		return fail(err)
	}
	defer st.Close()
	if n, err := st.MarkInterrupted(); err != nil {
		return fail(err)
	} else if n > 0 {
		fmt.Printf("note: %d earlier request(s) were left running and are now marked INTERRUPTED\n", n)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	start := time.Now()
	o := &orchestrator.Orchestrator{
		Engine:  &claudecode.Engine{},
		Store:   st,
		DataDir: *dataDir,
		Log: func(role, line string) {
			fmt.Printf("[%6.1fs] %-6s %s\n", time.Since(start).Seconds(), role, line)
		},
	}
	out, err := o.Run(ctx, orchestrator.Config{
		Repo: *repo, Request: text, Checks: checks, Protect: protect,
		PMModel: *pmModel, DevModel: *devModel, MaxFixRounds: *fixRounds, BudgetUSD: *budget,
		TaskTimeout: *taskTO, CheckTimeout: *checkTO, Solo: *solo, Review: *reviewFlag, QAModel: *qaModel,
		Mode: *mode, ProjectNotes: *notes, Panels: panels(*panel),
	})
	if err != nil {
		return fail(err)
	}

	diffStat, _ := out.Worktree.DiffStat()
	md, err := report.Markdown(st.DB(), out.RequestID, diffStat)
	if err != nil {
		return fail(err)
	}
	os.WriteFile(filepath.Join(out.Dir, "report.md"), []byte(md), 0o644)
	fmt.Println()
	fmt.Println(md)
	if out.Status == store.ReqDone {
		fmt.Printf("Review:  git -C \"%s\" diff %.8s..%s\n", out.Worktree.Repo, out.Worktree.Base, out.Worktree.Branch)
		fmt.Printf("Apply:   git -C \"%s\" merge %s\n", out.Worktree.Repo, out.Worktree.Branch)
		fmt.Printf("Discard: git -C \"%s\" worktree remove --force \"%s\" && git -C \"%s\" branch -D %s\n",
			out.Worktree.Repo, out.Worktree.Dir, out.Worktree.Repo, out.Worktree.Branch)
		return 0
	}
	return 1
}

// panels parses "Name:model,Name:model" into a review panel.
func panels(spec string) map[string][]orchestrator.Agent {
	if strings.TrimSpace(spec) == "" {
		return nil
	}
	var p []orchestrator.Agent
	for _, part := range strings.Split(spec, ",") {
		name, model, _ := strings.Cut(strings.TrimSpace(part), ":")
		if name == "" {
			continue
		}
		if model == "" {
			model = "sonnet"
		}
		ag := orchestrator.Agent{Name: name, Model: model}
		// "Name:model@ollama" = a model served by Ollama on this PC, on the light
		// local engine; "@ollama-cc" runs it inside Claude Code instead.
		cc := strings.HasSuffix(model, "@ollama-cc")
		if m, ok := strings.CutSuffix(strings.TrimSuffix(model, "-cc"), "@ollama"); ok {
			ag.Model = m
			ag.Env = ollama.Env(m)
			ag.Local = true
			ag.Native = !cc
			ag.ProfileDir = filepath.Join(defaultDataDir(), "profiles", "ollama-"+strings.NewReplacer(":", "_", "/", "_").Replace(m))
		}
		p = append(p, ag)
	}
	return map[string][]orchestrator.Agent{"review": p}
}

func defaultDataDir() string {
	if d := os.Getenv("LOCALAPPDATA"); d != "" {
		return filepath.Join(d, filepath.Base(appdir.Dir()))
	}
	return "codebanjang-data"
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "error:", err)
	return 1
}
