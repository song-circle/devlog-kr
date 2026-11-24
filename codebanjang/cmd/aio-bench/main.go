// aio-bench runs the same task many times (team vs solo, per model), grades
// every result with hidden tests the agents never saw, and prints a table.
//
//	aio-bench -task demos\ledger -modes solo,team,review -models sonnet -n 3
//
// A task folder holds the starting files plus task.json:
//
//	{"request": "...", "checks": ["go test ./..."], "protect": ["*_test.go"],
//	 "grade": {"files": "hidden", "command": "go test -v ./..."}}
//
// The grading files and any "reference" folder are never copied into the
// repository the agents work on.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/song-circle/devlog-kr/codebanjang/internal/appdir"
	"github.com/song-circle/devlog-kr/codebanjang/internal/proc"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"github.com/song-circle/devlog-kr/codebanjang/internal/check"
	"github.com/song-circle/devlog-kr/codebanjang/internal/engine/claudecode"
	"github.com/song-circle/devlog-kr/codebanjang/internal/orchestrator"
	"github.com/song-circle/devlog-kr/codebanjang/internal/store"
)

type taskFile struct {
	Request string   `json:"request"`
	Checks  []string `json:"checks"`
	Protect []string `json:"protect"`
	Grade   struct {
		Files   string `json:"files"`
		Command string `json:"command"`
	} `json:"grade"`
}

func main() { os.Exit(run()) }

func run() int {
	var (
		taskDir   = flag.String("task", "", "task folder with task.json (required)")
		modes     = flag.String("modes", "solo,team,review", "comma list: solo (developer only), team (PM+developer+checks), review (team + QA reviewer)")
		models    = flag.String("models", "sonnet", "comma list of models (PM and developer use the same one)")
		n         = flag.Int("n", 3, "repetitions per mode and model")
		budget    = flag.Float64("budget", 2.0, "max USD per request")
		fixRounds = flag.Int("fix-rounds", 3, "max fix rounds per request")
		taskTO    = flag.Duration("task-timeout", 10*time.Minute, "max time per agent run")
		dataDir   = flag.String("data", defaultDataDir(), "worktrees, logs and database")
		quiet     = flag.Bool("q", false, "print only one line per repetition")
	)
	flag.Parse()
	windows.SetConsoleOutputCP(65001)
	if *taskDir == "" {
		flag.PrintDefaults()
		return 2
	}
	var tf taskFile
	b, err := os.ReadFile(filepath.Join(*taskDir, "task.json"))
	if err != nil {
		return fail(err)
	}
	if err := json.Unmarshal(b, &tf); err != nil {
		return fail(fmt.Errorf("task.json: %w", err))
	}
	taskName := filepath.Base(filepath.Clean(*taskDir))

	benchID := time.Now().Format("20060102-150405")
	benchDir := filepath.Join(*dataDir, "bench", benchID)
	repo := filepath.Join(benchDir, "repo")
	if err := makeRepo(*taskDir, repo, tf.Grade.Files); err != nil {
		return fail(err)
	}
	st, err := store.Open(filepath.Join(*dataDir, "aio.db"))
	if err != nil {
		return fail(err)
	}
	defer st.Close()
	st.MarkInterrupted()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Printf("bench %s  task %s  repo %s\n", benchID, taskName, repo)
	for _, model := range split(*models) {
		for _, mode := range split(*modes) {
			for rep := 1; rep <= *n; rep++ {
				if ctx.Err() != nil {
					break
				}
				start := time.Now()
				o := &orchestrator.Orchestrator{Engine: &claudecode.Engine{}, Store: st, DataDir: *dataDir}
				if !*quiet {
					o.Log = func(role, line string) {
						fmt.Printf("    [%6.1fs] %-6s %s\n", time.Since(start).Seconds(), role, line)
					}
				}
				out, err := o.Run(ctx, orchestrator.Config{
					Repo: repo, Request: tf.Request, Checks: tf.Checks, Protect: tf.Protect,
					PMModel: model, DevModel: model, MaxFixRounds: *fixRounds, BudgetUSD: *budget,
					TaskTimeout: *taskTO, CheckTimeout: 5 * time.Minute, Solo: mode == "solo", Review: mode == "review",
				})
				if err != nil {
					fmt.Fprintf(os.Stderr, "%s %s #%d: %v\n", mode, model, rep, err)
					continue
				}
				br := store.BenchRun{BenchID: benchID, Task: taskName, RequestID: out.RequestID, Mode: mode, Model: model, Rep: rep}
				if tf.Grade.Command != "" {
					g, err := grade(ctx, *taskDir, tf, out.Worktree.Dir, filepath.Join(out.Dir, "grade.txt"))
					if err != nil {
						fmt.Fprintln(os.Stderr, "grade:", err)
					} else {
						br.Graded, br.GradePassed, br.TestsPassed, br.TestsFailed, br.GradeLog = true, g.Passed(), g.pass, g.fail, g.log
					}
				}
				st.AddBenchRun(br)
				fmt.Printf("%-5s %-8s #%d  %-11s hidden %d/%d  %s\n", mode, model, rep, out.Status,
					br.TestsPassed, br.TestsPassed+br.TestsFailed, out.RequestID)
			}
		}
	}

	md, err := summary(st.DB(), benchID, taskName)
	if err != nil {
		return fail(err)
	}
	os.WriteFile(filepath.Join(benchDir, "summary.md"), []byte(md), 0o644)
	fmt.Println()
	fmt.Println(md)
	return 0
}

// makeRepo copies the task folder (minus task.json, the grading folder and
// any reference solution) into a fresh git repository.
func makeRepo(src, dst, gradeDir string) error {
	skip := map[string]bool{"task.json": true, "reference": true}
	if gradeDir != "" {
		skip[filepath.Clean(gradeDir)] = true
	}
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		top := strings.Split(rel, string(filepath.Separator))[0]
		if skip[top] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		return err
	}
	for _, args := range [][]string{
		{"init", "-q"}, {"add", "-A"},
		{"-c", "user.name=코드반장", "-c", "user.email=codebanjang@localhost", "commit", "-qm", "task start"},
	} {
		if out, err := proc.NoWindow(exec.Command("git", append([]string{"-C", dst}, args...)...)).CombinedOutput(); err != nil {
			return fmt.Errorf("git %v: %v %s", args, err, out)
		}
	}
	return nil
}

type gradeResult struct {
	*check.Result
	pass, fail int
	log        string
}

var (
	passRe = regexp.MustCompile(`(?m)^\s*--- PASS`)
	failRe = regexp.MustCompile(`(?m)^\s*--- FAIL`)
)

// grade copies the hidden files into the worktree, runs the grading command
// and removes the files again, so the request branch stays as the team left it.
func grade(ctx context.Context, taskDir string, tf taskFile, worktree, logPath string) (*gradeResult, error) {
	var copied []string
	if tf.Grade.Files != "" {
		src := filepath.Join(taskDir, tf.Grade.Files)
		err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(src, p)
			dst := filepath.Join(worktree, rel)
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			os.MkdirAll(filepath.Dir(dst), 0o755)
			copied = append(copied, dst)
			return os.WriteFile(dst, b, 0o644)
		})
		if err != nil {
			return nil, err
		}
	}
	defer func() {
		for _, p := range copied {
			os.Remove(p)
		}
	}()
	r := check.Run(ctx, worktree, tf.Grade.Command, 5*time.Minute, logPath)
	return &gradeResult{Result: r, pass: len(passRe.FindAllString(r.Output, -1)), fail: len(failRe.FindAllString(r.Output, -1)), log: logPath}, nil
}

// summary aggregates bench_runs joined with runs/tasks/checks.
func summary(db *sql.DB, benchID, task string) (string, error) {
	rows, err := db.Query(`
		SELECT b.mode, b.model, b.request_id, rq.status, b.grade_passed, b.tests_passed, b.tests_failed,
			COALESCE((SELECT SUM(cost_usd) FROM runs WHERE request_id=b.request_id),0),
			COALESCE((SELECT SUM(duration_ms) FROM runs WHERE request_id=b.request_id),0),
			(SELECT COUNT(*) FROM tasks WHERE request_id=b.request_id AND kind='fix'),
			(SELECT COUNT(*) FROM tasks WHERE request_id=b.request_id AND kind='work'),
			(SELECT COUNT(*) FROM checks WHERE request_id=b.request_id AND protected_changes<>'')
		FROM bench_runs b JOIN requests rq ON rq.id=b.request_id
		WHERE b.bench_id=? ORDER BY b.id`, benchID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type agg struct {
		n, done, hidden, fixes, work, tamper int
		tp, tt                               int
		costs, times                         []float64
	}
	groups := map[string]*agg{}
	var order []string
	var detail strings.Builder
	detail.WriteString("| mode | model | request | status | hidden tests | cost | time | fix rounds | tasks |\n|---|---|---|---|---|---|---|---|---|\n")
	for rows.Next() {
		var mode, model, req, status string
		var gp bool
		var tp, tf, fixes, work, tamper int
		var cost float64
		var ms int64
		if err := rows.Scan(&mode, &model, &req, &status, &gp, &tp, &tf, &cost, &ms, &fixes, &work, &tamper); err != nil {
			return "", err
		}
		k := mode + " / " + model
		g := groups[k]
		if g == nil {
			g = &agg{}
			groups[k] = g
			order = append(order, k)
		}
		g.n++
		if status == store.ReqDone {
			g.done++
		}
		if gp {
			g.hidden++
		}
		g.fixes += fixes
		g.work += work
		g.tamper += tamper
		g.tp += tp
		g.tt += tp + tf
		g.costs = append(g.costs, cost)
		g.times = append(g.times, float64(ms)/1000)
		fmt.Fprintf(&detail, "| %s | %s | %s | %s | %d/%d | $%.4f | %.0fs | %d | %d |\n", mode, model, req, status, tp, tp+tf, cost, float64(ms)/1000, fixes, work)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Bench %s — %s\n\n", benchID, task)
	b.WriteString("| mode / model | runs | checks passed | hidden all pass | hidden tests (sum) | median cost | median time | fix rounds (avg) | dev tasks (avg) | protected edits |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|\n")
	for _, k := range order {
		g := groups[k]
		fmt.Fprintf(&b, "| %s | %d | %d/%d | %d/%d | %d/%d | $%.4f | %.0fs | %.1f | %.1f | %d |\n",
			k, g.n, g.done, g.n, g.hidden, g.n, g.tp, g.tt, median(g.costs), median(g.times),
			float64(g.fixes)/float64(g.n), float64(g.work)/float64(g.n), g.tamper)
	}
	b.WriteString("\nhidden = grading tests the agents never saw. cost = engine list-price estimate.\n\n## Runs\n\n")
	b.WriteString(detail.String())
	return b.String(), nil
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func split(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
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
