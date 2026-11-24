// aio-probe runs one coding-agent task in an isolated git worktree and reports
// what happened: events, changed files, tokens, cost, time, and an optional
// check command (build/test) run afterwards.
//
//	aio-probe -repo C:\path\to\repo -prompt "add a README" [-check "go test ./..."]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/song-circle/devlog-kr/codebanjang/internal/appdir"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"github.com/song-circle/devlog-kr/codebanjang/internal/engine"
	"github.com/song-circle/devlog-kr/codebanjang/internal/engine/claudecode"
	"github.com/song-circle/devlog-kr/codebanjang/internal/proc"
	"github.com/song-circle/devlog-kr/codebanjang/internal/workspace"
)

type summary struct {
	ID            string             `json:"id"`
	Repo          string             `json:"repo"`
	Worktree      string             `json:"worktree"`
	Branch        string             `json:"branch"`
	Base          string             `json:"base"`
	Prompt        string             `json:"prompt"`
	Engine        string             `json:"engine"`
	Model         string             `json:"model"`
	Args          []string           `json:"args"`
	Result        engine.Result      `json:"result"`
	Changes       []workspace.Change `json:"changes"`
	OutsideWrites []string           `json:"outside_writes"` // write-tool targets outside the worktree
	RepoChanged   bool               `json:"repo_changed"`   // original working folder changed during the run
	Check         *checkResult       `json:"check,omitempty"`
}

type checkResult struct {
	Command  string        `json:"command"`
	ExitCode int           `json:"exit_code"`
	TimedOut bool          `json:"timed_out"`
	Duration time.Duration `json:"duration"`
}

func main() {
	os.Exit(run())
}

func run() int {
	var (
		repo       = flag.String("repo", "", "git repository to work on (required)")
		prompt     = flag.String("prompt", "", "task text")
		promptFile = flag.String("prompt-file", "", "read task text from a UTF-8 file")
		model      = flag.String("model", "sonnet", "model alias or name")
		tools      = flag.String("tools", "Read,Edit,Write,Glob,Grep", "tools the agent may use")
		budget     = flag.Float64("budget", 1.0, "max USD per run (0 = no limit)")
		timeout    = flag.Duration("timeout", 10*time.Minute, "max run time")
		check      = flag.String("check", "", "command run in the worktree afterwards, e.g. \"go test ./...\"")
		checkTO    = flag.Duration("check-timeout", 5*time.Minute, "max check time")
		dataDir    = flag.String("data", defaultDataDir(), "where worktrees and run logs go")
		cleanup    = flag.Bool("cleanup", false, "remove the worktree and branch afterwards")
		quiet      = flag.Bool("q", false, "do not print assistant text")
	)
	flag.Parse()
	windows.SetConsoleOutputCP(65001) // agent text is often not ASCII

	if *repo == "" || (*prompt == "") == (*promptFile == "") {
		fmt.Fprintln(os.Stderr, "usage: aio-probe -repo DIR (-prompt TEXT | -prompt-file FILE) [options]")
		flag.PrintDefaults()
		return 2
	}
	task := *prompt
	if *promptFile != "" {
		b, err := os.ReadFile(*promptFile)
		if err != nil {
			return fail(err)
		}
		task = string(b)
	}

	id := time.Now().Format("20060102-150405")
	top, err := workspace.TopLevel(*repo)
	if err != nil {
		return fail(err)
	}
	before, err := workspace.Status(top)
	if err != nil {
		return fail(err)
	}
	if len(before) > 0 {
		fmt.Printf("note: %s has %d uncommitted change(s); the worktree starts from HEAD without them\n", top, len(before))
	}

	wt, err := workspace.Create(top, filepath.Join(*dataDir, "worktrees"), id)
	if err != nil {
		return fail(err)
	}
	logDir := filepath.Join(*dataDir, "runs", id)
	fmt.Printf("run      %s\nworktree %s\nbranch   %s (from %.8s)\n\n", id, wt.Dir, wt.Branch, wt.Base)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	spec := engine.Spec{
		Prompt:       task,
		Dir:          wt.Dir,
		Model:        *model,
		Tools:        splitList(*tools),
		MaxBudgetUSD: *budget,
		Timeout:      *timeout,
		LogDir:       logDir,
	}
	eng := &claudecode.Engine{}
	var outside []string
	start := time.Now()
	res, runErr := eng.Run(ctx, spec, func(ev engine.Event) {
		t := ev.Time.Sub(start).Seconds()
		switch ev.Kind {
		case engine.EventInit:
			fmt.Printf("[%6.1fs] start   model=%s\n", t, ev.Text)
		case engine.EventText:
			if !*quiet {
				fmt.Printf("[%6.1fs] say     %s\n", t, oneLine(ev.Text, 160))
			}
		case engine.EventToolUse:
			mark := ""
			if isWriteTool(ev.Tool) && ev.Target != "" && !inside(wt.Dir, ev.Target) {
				outside = append(outside, ev.Target)
				mark = "  << OUTSIDE WORKTREE"
			}
			fmt.Printf("[%6.1fs] tool    %-6s %s%s\n", t, ev.Tool, rel(wt.Dir, ev.Target), mark)
		case engine.EventToolError:
			fmt.Printf("[%6.1fs] DENIED/ERROR %s\n", t, oneLine(ev.Text, 160))
		}
	})
	if runErr != nil {
		fmt.Fprintln(os.Stderr, "engine error:", runErr)
	}

	sum := summary{
		ID: id, Repo: top, Worktree: wt.Dir, Branch: wt.Branch, Base: wt.Base,
		Prompt: task, Engine: eng.Name(), Model: *model, Args: claudecode.Args(spec),
		Result: res, OutsideWrites: outside,
	}
	if sum.Changes, err = workspace.Status(wt.Dir); err != nil {
		fmt.Fprintln(os.Stderr, "status:", err)
	}
	after, err := workspace.Status(top)
	if err != nil {
		fmt.Fprintln(os.Stderr, "status:", err)
	}
	sum.RepoChanged = !sameChanges(before, after)

	if *check != "" && res.Status == engine.StatusSuccess {
		sum.Check = runCheck(ctx, wt.Dir, *check, *checkTO, filepath.Join(logDir, "check.txt"))
	}

	if diff, err := wt.Diff(); err == nil {
		os.WriteFile(filepath.Join(logDir, "diff.patch"), []byte(diff), 0o644)
	}
	if b, err := json.MarshalIndent(sum, "", "  "); err == nil {
		os.WriteFile(filepath.Join(logDir, "summary.json"), b, 0o644)
	}
	printSummary(sum, logDir)

	if *cleanup {
		if err := wt.Remove(true); err != nil {
			fmt.Fprintln(os.Stderr, "cleanup:", err)
		}
	}
	if res.Status != engine.StatusSuccess || (sum.Check != nil && sum.Check.ExitCode != 0) {
		return 1
	}
	return 0
}

func printSummary(s summary, logDir string) {
	r := s.Result
	fmt.Printf("\n--- result ---\n")
	fmt.Printf("status     %s (%s), exit %d\n", r.Status, r.Subtype, r.ExitCode)
	fmt.Printf("time       %.1fs, %d turns, %d tool calls, %d tool errors, %d denials\n",
		r.Duration.Seconds(), r.Turns, r.ToolCalls, r.ToolErrors, r.Denials)
	fmt.Printf("tokens     in %d, out %d, cache read %d, cache write %d\n",
		r.Usage.InputTokens, r.Usage.OutputTokens, r.Usage.CacheReadTokens, r.Usage.CacheCreateTokens)
	if r.Usage.CostEstimated {
		fmt.Printf("cost       $%.4f (run did not finish: summed per response at list price, engine side calls not included)\n", r.Usage.CostUSD)
		if len(r.Usage.UnpricedModels) > 0 {
			fmt.Printf("           no price known for %s, not counted\n", strings.Join(r.Usage.UnpricedModels, ", "))
		}
	} else {
		fmt.Printf("cost       $%.4f (list-price estimate reported by the engine)\n", r.Usage.CostUSD)
	}
	fmt.Printf("changes    %d file(s)\n", len(s.Changes))
	for _, c := range s.Changes {
		fmt.Printf("           %s %s\n", c.Status, c.Path)
	}
	if len(s.OutsideWrites) > 0 {
		fmt.Printf("OUTSIDE    %d write attempt(s) outside the worktree\n", len(s.OutsideWrites))
	}
	if s.RepoChanged {
		fmt.Printf("WARNING    the original folder %s changed during the run\n", s.Repo)
	} else {
		fmt.Printf("original   unchanged\n")
	}
	if c := s.Check; c != nil {
		state := "PASS"
		if c.TimedOut {
			state = "TIMEOUT"
		} else if c.ExitCode != 0 {
			state = "FAIL"
		}
		fmt.Printf("check      %s (exit %d, %.1fs): %s\n", state, c.ExitCode, c.Duration.Seconds(), c.Command)
	}
	fmt.Printf("logs       %s\n", logDir)
}

// runCheck runs a user-given command line through cmd.exe inside a job.
func runCheck(ctx context.Context, dir, command string, timeout time.Duration, logPath string) *checkResult {
	res := &checkResult{Command: command, ExitCode: -1}
	f, err := os.Create(logPath)
	if err != nil {
		return res
	}
	defer f.Close()

	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /S /C "` + command + `"`}
	cmd.Dir = dir
	cmd.Stdout = f
	cmd.Stderr = f
	job, err := proc.NewJob(0)
	if err != nil {
		fmt.Fprintf(f, "job: %v\n", err)
		return res
	}
	defer job.Close()
	start := time.Now()
	if err := job.Start(cmd); err != nil {
		fmt.Fprintf(f, "start: %v\n", err)
		return res
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan struct{})
	go func() {
		select {
		case <-cctx.Done():
			job.Kill()
		case <-done:
		}
	}()
	cmd.Wait()
	close(done)
	res.Duration = time.Since(start)
	res.TimedOut = cctx.Err() == context.DeadlineExceeded
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	return res
}

func defaultDataDir() string {
	if d := os.Getenv("LOCALAPPDATA"); d != "" {
		return filepath.Join(d, filepath.Base(appdir.Dir()))
	}
	return "codebanjang-data"
}

func isWriteTool(name string) bool {
	switch name {
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		return true
	}
	return false
}

// inside reports whether path p is within dir (case-insensitive on Windows).
func inside(dir, p string) bool {
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	r, err := filepath.Rel(strings.ToLower(filepath.Clean(dir)), strings.ToLower(filepath.Clean(p)))
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}

func rel(dir, p string) string {
	if p == "" || !filepath.IsAbs(p) || !inside(dir, p) {
		return p
	}
	if r, err := filepath.Rel(dir, p); err == nil {
		return r
	}
	return p
}

func sameChanges(a, b []workspace.Change) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		out = append(out, f)
	}
	return out
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "..."
	}
	return s
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "error:", err)
	return 1
}
