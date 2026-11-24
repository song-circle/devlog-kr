// Package orchestrator runs one request through the team:
// PM plan → developer tasks in order → system checks → fix rounds → report.
// Every step is recorded in the store; limits (rounds, money, time) are
// enforced here, not left to the agents.
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/song-circle/devlog-kr/ai-office/internal/check"
	"github.com/song-circle/devlog-kr/ai-office/internal/engine"
	"github.com/song-circle/devlog-kr/ai-office/internal/knowledge"
	"github.com/song-circle/devlog-kr/ai-office/internal/ollama"
	"github.com/song-circle/devlog-kr/ai-office/internal/store"
	"github.com/song-circle/devlog-kr/ai-office/internal/workspace"
)

// Config for one request.
type Config struct {
	ID           string        `json:"-"` // request id; empty = NewID()
	Repo         string        `json:"repo"`
	Request      string        `json:"request"`
	Checks       []string      `json:"checks"`         // user-defined commands; all must pass
	Protect      []string      `json:"protect"`        // globs the agents may not change
	PMModel      string        `json:"pm_model"`       // engine model for the PM
	DevModel     string        `json:"dev_model"`      // engine model for the developer
	MaxFixRounds int           `json:"max_fix_rounds"` // fix tasks after failed checks
	BudgetUSD    float64       `json:"budget_usd"`     // whole request; runs stop when spent
	TaskTimeout  time.Duration `json:"task_timeout"`
	CheckTimeout time.Duration `json:"check_timeout"`
	Solo         bool          `json:"solo"`     // skip the PM: one task = the request itself
	Review       bool          `json:"review"`   // after checks pass, a QA agent reviews the diff (team mode only)
	QAModel      string        `json:"qa_model"` // engine model for the reviewer (default: DevModel)
	// Agents assigns an employee to a role ("pm", "dev", "qa"). Its model
	// and account override the fields above; missing roles use them as is.
	Agents map[string]Agent `json:"agents,omitempty"`
	// Mode: ask / plan / audit (read-only, no branch) or solo / team / review.
	// Empty = derived from Solo and Review.
	Mode string `json:"mode,omitempty"`
	// ProjectID and ProjectNotes come from the registered project; the notes
	// (stack, rules, background) are added to every employee's instructions.
	ProjectID    int64  `json:"project_id,omitempty"`
	ProjectNotes string `json:"project_notes,omitempty"`
	// Knowledge is the project's knowledge document (.aioffice/knowledge.md),
	// added to every employee's instructions; in learn mode it is the
	// document being updated instead.
	Knowledge string `json:"-"`
	// Preparing the work folder of code-changing modes before anyone works
	// in it: files git ignores that the project needs are copied from the
	// project folder, then the setup commands run (SetupTimeout each).
	CopyFiles    []string      `json:"copy_files,omitempty"`
	Setup        []string      `json:"setup,omitempty"`
	SetupTimeout time.Duration `json:"setup_timeout,omitempty"`
	// Panels hold meetings instead of single employees: "review" (QA review
	// stage and the audit mode) is a cross-review when it has 2+ members.
	// "plan" (planning in plan and team modes) is a draft-critique meeting
	// and "meeting" (meeting mode) a discussion, both with 2+ members.
	Panels        map[string][]Agent `json:"panels,omitempty"`
	MeetingRounds int                `json:"meeting_rounds,omitempty"` // default 2
}

// Modes.
const (
	ModeAsk     = "ask"     // answer a question about the project
	ModePlan    = "plan"    // PM writes a plan / spec, no code
	ModeAudit   = "audit"   // QA reviews the current code, no changes
	ModeSolo    = "solo"    // developer alone
	ModeTeam    = "team"    // PM + developer + checks
	ModeReview  = "review"  // team + QA reviewer
	ModeMeeting = "meeting" // several employees discuss, a moderator concludes
	ModeLearn   = "learn"   // read the project and write its knowledge document
)

// ReadOnly reports whether a mode only reads the project (no worktree, no branch).
func ReadOnly(mode string) bool {
	return mode == ModeAsk || mode == ModePlan || mode == ModeAudit || mode == ModeMeeting || mode == ModeLearn
}

func (c *Config) mode() string {
	if c.Mode != "" {
		return c.Mode
	}
	switch {
	case c.Solo:
		return ModeSolo
	case c.Review:
		return ModeReview
	}
	return ModeTeam
}

// Agent is the employee doing a role: which account, which model, and any
// extra instructions on top of the role's own.
type Agent struct {
	Name       string   `json:"name"`
	Model      string   `json:"model"`
	ProfileDir string   `json:"-"` // engine account folder; empty = default login
	Prompt     string   `json:"prompt,omitempty"`
	Env        []string `json:"-"`                // extra engine environment (local model server)
	Local      bool     `json:"local,omitempty"`  // runs on this PC (no cost)
	Native     bool     `json:"native,omitempty"` // use the light local engine instead of Claude Code
}

// NewID returns a request id: local time to the millisecond, usable in a
// branch name and a folder name.
func NewID() string {
	t := time.Now()
	return t.Format("20060102-150405") + fmt.Sprintf("-%03d", t.Nanosecond()/1e6)
}

// Logger receives progress lines for display.
type Logger func(role, line string)

// Orchestrator holds the shared parts.
type Orchestrator struct {
	Engine  engine.Engine
	Store   *store.Store
	DataDir string
	Log     Logger
}

// Outcome is what Run returns.
type Outcome struct {
	RequestID string
	Status    string
	Reason    string
	Worktree  *workspace.Worktree
	Dir       string // request log directory
}

var errBudget = errors.New("요청 예산을 다 써서 멈춤")

type run struct {
	o        *Orchestrator
	cfg      Config
	id       string
	wt       *workspace.Worktree
	dir      string
	mu       sync.Mutex // guards runNo (meeting members run in parallel)
	runNo    int
	meetings []int64
}

// Run executes one request to the end and returns its outcome.
func (o *Orchestrator) Run(ctx context.Context, cfg Config) (*Outcome, error) {
	p, err := o.Prepare(cfg)
	if err != nil {
		return nil, err
	}
	return p.Execute(ctx)
}

// Prepared is a request whose worktree and database row already exist.
type Prepared struct {
	r *run
}

// ID of the prepared request.
func (p *Prepared) ID() string { return p.r.id }

// Prepare creates the worktree and records the request, so callers (the app)
// can show it before any agent starts. Execute runs it.
func (o *Orchestrator) Prepare(cfg Config) (*Prepared, error) {
	if cfg.MaxFixRounds < 0 {
		cfg.MaxFixRounds = 0
	}
	if cfg.BudgetUSD <= 0 {
		cfg.BudgetUSD = 2
	}
	id := cfg.ID
	if id == "" {
		id = NewID()
	}
	cfg.Mode = cfg.mode()
	cfg.Solo = cfg.Mode == ModeSolo
	cfg.Review = cfg.Mode == ModeReview
	var wt *workspace.Worktree
	var err error
	if ReadOnly(cfg.Mode) {
		// Agents only read: they work in the project folder itself (current
		// files, uncommitted changes included) and no branch is made.
		wt, err = workspace.ReadOnly(cfg.Repo)
	} else {
		wt, err = workspace.Create(cfg.Repo, filepath.Join(o.DataDir, "worktrees"), id)
	}
	if err != nil {
		return nil, err
	}
	r := &run{o: o, cfg: cfg, id: id, wt: wt, dir: filepath.Join(o.DataDir, "requests", id)}
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		return nil, err
	}
	cj, _ := json.Marshal(cfg)
	if err := o.Store.CreateRequest(store.Request{ID: id, Text: cfg.Request, Repo: wt.Repo, Worktree: wt.Dir,
		Branch: wt.Branch, Base: wt.Base, ConfigJSON: string(cj), Mode: cfg.Mode, ProjectID: cfg.ProjectID}); err != nil {
		return nil, err
	}
	return &Prepared{r: r}, nil
}

// Execute runs a prepared request to the end.
func (p *Prepared) Execute(ctx context.Context) (*Outcome, error) {
	r, o, id, wt := p.r, p.r.o, p.r.id, p.r.wt
	if ReadOnly(r.cfg.Mode) {
		o.log("system", fmt.Sprintf("request %s  %s (read only)  %s", id, r.cfg.Mode, wt.Dir))
	} else {
		o.log("system", fmt.Sprintf("request %s  worktree %s  branch %s", id, wt.Dir, wt.Branch))
	}

	var status, reason string
	if ReadOnly(r.cfg.Mode) {
		status, reason = r.answer(ctx)
	} else {
		status, reason = r.execute(ctx)
	}
	if ctx.Err() != nil {
		status, reason = store.ReqCancelled, "stopped by user"
	}
	if status == store.ReqDone && !ReadOnly(r.cfg.Mode) {
		if err := r.commit(); err != nil {
			status, reason = store.ReqFailed, "commit: "+err.Error()
		}
	}
	if err := o.Store.FinishRequest(id, status, reason); err != nil {
		return nil, err
	}
	return &Outcome{RequestID: id, Status: status, Reason: reason, Worktree: wt, Dir: r.dir}, nil
}

func (o *Orchestrator) log(role, line string) {
	if o.Log != nil {
		o.Log(role, line)
	}
}

// execute returns the final request status and reason.
func (r *run) execute(ctx context.Context) (string, string) {
	if err := r.prepare(ctx); err != nil {
		return store.ReqFailed, "작업 폴더 준비: " + err.Error()
	}
	p, err := r.plan(ctx)
	if err != nil {
		return store.ReqFailed, "plan: " + err.Error()
	}

	// Developer tasks, in order.
	var reports []string
	for i, t := range p.Tasks {
		taskID, err := r.o.Store.AddTask(r.id, i+1, "work", "dev", t.Title, t.Instructions)
		if err != nil {
			return store.ReqFailed, err.Error()
		}
		res, err := r.runTask(ctx, taskID, "dev", r.cfg.DevModel, devPrompt(r.cfg.Request, p, i, reports), devSystemPrompt(r.cfg.Checks, r.cfg.Protect), "")
		if err != nil {
			return store.ReqFailed, fmt.Sprintf("task %d: %v", i+1, err)
		}
		reports = append(reports, res.Text)
		r.o.Store.SetTaskStatus(taskID, store.Completed, "")
	}

	// Checks and fix rounds.
	for round := 0; ; round++ {
		failures, err := r.verify(ctx, round)
		if err != nil {
			return store.ReqFailed, err.Error()
		}
		if failures == "" && r.cfg.Review && !r.cfg.Solo {
			if panel := r.cfg.Panels["review"]; len(panel) >= 2 {
				diff, derr := r.wt.Diff()
				if derr != nil {
					return store.ReqFailed, derr.Error()
				}
				if len(diff) > maxReviewDiff {
					diff = diff[:maxReviewDiff] + "\n... (diff truncated; read the files for the rest)"
				}
				cr, cerr := r.crossReview(ctx, panel, fmt.Sprintf("QA 교차 검토 %d회차", round+1),
					reviewPrompt(r.cfg.Request, diff), reviewSystemPrompt(r.cfg.Checks, r.cfg.Protect))
				if cerr != nil {
					return store.ReqFailed, "review: " + cerr.Error()
				}
				failures = failuresFrom(cr)
			} else if failures, err = r.review(ctx, round); err != nil {
				return store.ReqFailed, "review: " + err.Error()
			}
		}
		if failures == "" {
			return store.ReqDone, ""
		}
		if round >= r.cfg.MaxFixRounds {
			return store.ReqFailed, fmt.Sprintf("verification still failing after %d fix round(s)", round)
		}
		taskID, err := r.o.Store.AddTask(r.id, len(p.Tasks)+round+1, "fix", "dev",
			fmt.Sprintf("검사 실패 수정 %d회차", round+1), failures)
		if err != nil {
			return store.ReqFailed, err.Error()
		}
		r.o.Store.SetTaskStatus(taskID, store.Revision, "checks failed")
		if _, err := r.runTask(ctx, taskID, "dev", r.cfg.DevModel, fixPrompt(r.cfg.Request, round+1, failures),
			devSystemPrompt(r.cfg.Checks, r.cfg.Protect), ""); err != nil {
			return store.ReqFailed, fmt.Sprintf("fix %d: %v", round+1, err)
		}
		r.o.Store.SetTaskStatus(taskID, store.Completed, "")
	}
}

// plan asks the PM for tasks (or, in solo mode, uses the request as one task).
func (r *run) plan(ctx context.Context) (*plan, error) {
	if r.cfg.Solo {
		p := &plan{Summary: "(solo) 요청 전체를 작업 하나로 수행", Tasks: []planTask{{Title: "요청 수행", Instructions: r.cfg.Request}}}
		r.o.Store.SetPlanSummary(r.id, p.Summary)
		return p, nil
	}
	taskID, err := r.o.Store.AddTask(r.id, 0, "plan", "pm", "계획 수립", r.cfg.Request)
	if err != nil {
		return nil, err
	}
	var res engine.Result
	if panel := r.cfg.Panels["plan"]; len(panel) >= 2 {
		res, _, err = r.draftCritique(ctx, panel, "기획 회의", pmPrompt(r.cfg.Request), pmSystemPrompt(r.cfg.Checks, r.cfg.Protect), planSchema)
	} else {
		res, err = r.runTask(ctx, taskID, "pm", r.cfg.PMModel, pmPrompt(r.cfg.Request), pmSystemPrompt(r.cfg.Checks, r.cfg.Protect), planSchema)
	}
	if err != nil {
		return nil, err
	}
	var p plan
	if err := decodeAnswer(res, &p); err != nil || len(p.Tasks) == 0 {
		r.o.Store.SetTaskStatus(taskID, store.Failed, "plan was not valid JSON")
		return nil, fmt.Errorf("PM did not return a valid plan: %v", err)
	}
	if len(p.Tasks) > 5 {
		p.Tasks = p.Tasks[:5]
	}
	r.o.Store.SetPlanSummary(r.id, p.Summary)
	r.o.Store.SetTaskStatus(taskID, store.Completed, fmt.Sprintf("%d task(s)", len(p.Tasks)))
	r.o.log("pm", "plan: "+p.Summary)
	for i, t := range p.Tasks {
		r.o.log("pm", fmt.Sprintf("  %d. %s  [%s]", i+1, t.Title, strings.Join(t.Files, ", ")))
	}
	return &p, nil
}

// runTask runs the engine once for a task (as the employee assigned to the
// role) and records everything.
func (r *run) runTask(ctx context.Context, taskID int64, role, model, prompt, system, schema string) (engine.Result, error) {
	return r.runAgent(ctx, taskID, role, r.cfg.Agents[role], model, prompt, system, schema, true)
}

// runAgent runs one employee once. manageTask=false leaves the task's status
// alone (a meeting is one task with many statements).
func (r *run) runAgent(ctx context.Context, taskID int64, role string, ag Agent, model, prompt, system, schema string, manageTask bool) (engine.Result, error) {
	if ag.Local {
		// One graphics card: local models take turns. Running several at once
		// makes Ollama swap them in and out until runs time out (bug 014).
		ollama.GPU.Lock()
		defer ollama.GPU.Unlock()
	}
	st := r.o.Store
	spent, _ := st.CostSoFar(r.id)
	remaining := r.cfg.BudgetUSD - spent
	if remaining < 0.01 {
		st.SetTaskStatus(taskID, store.Failed, "budget exhausted")
		return engine.Result{}, errBudget
	}
	if ag.Model != "" {
		model = ag.Model
	}
	if doc := knowledge.ForPrompt(r.cfg.Knowledge); doc != "" && r.cfg.Mode != ModeLearn {
		system += "\n\n프로젝트 지식 문서(" + knowledge.RelPath + ", 이 프로젝트에 대해 먼저 알아야 할 것. 코드와 다르면 코드가 맞다):\n" + doc
	}
	if notes := strings.TrimSpace(r.cfg.ProjectNotes); notes != "" {
		system += "\n\n프로젝트 설명(사용자가 등록한 배경·규칙, 반드시 따른다):\n" + notes
	}
	if ag.Prompt != "" {
		system += "\n\n이 직원에게만 주는 추가 지시:\n" + ag.Prompt
	}
	who := role
	if ag.Name != "" {
		who = ag.Name
	}
	if manageTask {
		st.SetTaskStatus(taskID, store.Assigned, who)
		st.SetTaskStatus(taskID, store.InProgress, "")
	}
	r.mu.Lock()
	r.runNo++
	logDir := filepath.Join(r.dir, fmt.Sprintf("%02d-%s", r.runNo, role))
	r.mu.Unlock()
	spec := engine.Spec{
		Prompt: prompt, Dir: r.wt.Dir, Model: model, MaxBudgetUSD: remaining,
		Timeout: r.cfg.TaskTimeout, LogDir: logDir, SystemPrompt: system, JSONSchema: schema,
		ProfileDir: ag.ProfileDir, Env: ag.Env,
	}
	if role != "dev" || ReadOnly(r.cfg.Mode) {
		spec.Tools = []string{"Read", "Glob", "Grep"} // planners, reviewers and read-only modes only read
	}
	speaker := role
	if ag.Name != "" && !manageTask {
		speaker = ag.Name // in meetings, tell the speakers apart
		r.o.log(speaker, "speaking")
	}
	start := time.Now()
	eng := r.o.Engine
	if ag.Native {
		eng = &ollama.Engine{}
	}
	res, runErr := eng.Run(ctx, spec, func(ev engine.Event) {
		switch ev.Kind {
		case engine.EventToolUse:
			r.o.log(speaker, fmt.Sprintf("%-6s %s", ev.Tool, rel(r.wt.Dir, ev.Target)))
		case engine.EventToolError:
			r.o.log(speaker, "도구 오류: "+oneLine(ev.Text, 140))
		}
	})
	if ag.Local {
		// Ran on this PC: the engine's figure is Anthropic list price times
		// the local model's tokens, not money anyone pays.
		res.Usage.CostUSD, res.Usage.CostEstimated = 0, false
	}
	st.AddRun(store.Run{
		RequestID: r.id, TaskID: taskID, Role: role, Engine: eng.Name(), Model: model, Status: string(res.Status),
		Turns: res.Turns, ToolCalls: res.ToolCalls, ToolErrors: res.ToolErrors, Denials: res.Denials,
		InputTokens: res.Usage.InputTokens, OutputTokens: res.Usage.OutputTokens,
		CacheRead: res.Usage.CacheReadTokens, CacheWrite: res.Usage.CacheCreateTokens,
		CostUSD: res.Usage.CostUSD, CostEstimated: res.Usage.CostEstimated, Agent: ag.Name,
		Duration: res.Duration, LogDir: logDir, ResultText: res.Text, StartedAt: start,
	})
	r.o.log(speaker, fmt.Sprintf("done: %s, %.0fs, $%.4f", res.Status, res.Duration.Seconds(), res.Usage.CostUSD))
	if manageTask {
		st.SetTaskResult(taskID, 1, res.Text)
	}
	if runErr != nil {
		st.SetTaskStatus(taskID, store.Failed, runErr.Error())
		return res, runErr
	}
	switch res.Status {
	case engine.StatusSuccess:
		return res, nil
	case engine.StatusCancelled:
		st.SetTaskStatus(taskID, store.Cancelled, "")
		return res, ctx.Err()
	default:
		note := fmt.Sprintf("engine %s (%s)", res.Status, res.Subtype)
		if res.Status == engine.StatusLimited {
			note = "AI 계정 사용 한도에 도달: " + oneLine(res.Text, 200)
		} else if res.Subtype == "error_max_budget_usd" {
			note = fmt.Sprintf("요청 예산 $%.2f를 다 써서 멈춤", r.cfg.BudgetUSD)
		} else if res.Status == engine.StatusError && strings.TrimSpace(res.Text) != "" {
			note = "engine error: " + oneLine(res.Text, 200)
		}
		st.SetTaskStatus(taskID, store.Failed, note)
		return res, errors.New(note)
	}
}

// verify reverts changes to protected files, runs every check, and returns
// a failure description for the developer ("" when everything passed).
func (r *run) verify(ctx context.Context, round int) (string, error) {
	var fails []string

	changes, err := workspace.Status(r.wt.Dir)
	if err != nil {
		return "", err
	}
	var touched []string
	for _, c := range changes {
		if workspace.MatchAny(r.cfg.Protect, c.Path) {
			touched = append(touched, c.Path)
			if err := r.wt.Restore(c.Path); err != nil {
				return "", fmt.Errorf("restore %s: %w", c.Path, err)
			}
		}
	}
	if len(touched) > 0 {
		r.o.log("qa", "protected file(s) changed, reverted: "+strings.Join(touched, ", "))
		fails = append(fails, "고치면 안 되는 파일을 바꿔서 시스템이 되돌렸다: "+strings.Join(touched, ", ")+
			". 이 파일은 그대로 두고 다른 코드를 고쳐서 해결하라.")
	}

	for i, cmd := range r.cfg.Checks {
		logPath := filepath.Join(r.dir, fmt.Sprintf("check-r%d-%d.txt", round, i+1))
		res := check.Run(ctx, r.wt.Dir, cmd, r.cfg.CheckTimeout, logPath)
		r.o.Store.AddCheck(store.Check{RequestID: r.id, Round: round, Command: cmd, ExitCode: res.ExitCode,
			TimedOut: res.TimedOut, Duration: res.Duration, ProtectedChanges: strings.Join(touched, ","), LogPath: logPath})
		state := "PASS"
		if !res.Passed() {
			state = "FAIL"
			if res.TimedOut {
				state = "TIMEOUT"
			}
			fails = append(fails, fmt.Sprintf("검사 `%s` 실패(exit %d):\n%s", cmd, res.ExitCode, check.Tail(res.Output, 3000)))
		}
		r.o.log("qa", fmt.Sprintf("check %s (%.1fs): %s", state, res.Duration.Seconds(), cmd))
	}
	return strings.Join(fails, "\n\n"), nil
}

// prepare readies the work folder before anyone works in it. A new
// worktree has only what git tracks, so builds and checks that need
// ignored files (.env, installed packages) would fail there. A setup that
// changes files git sees fails the request: those changes would be mixed
// into the AI's work and its commit.
func (r *run) prepare(ctx context.Context) error {
	if len(r.cfg.CopyFiles) == 0 && len(r.cfg.Setup) == 0 {
		return nil
	}
	st := r.o.Store
	plan := []string{}
	if len(r.cfg.CopyFiles) > 0 {
		plan = append(plan, "복사: "+strings.Join(r.cfg.CopyFiles, ", "))
	}
	for _, c := range r.cfg.Setup {
		plan = append(plan, "실행: "+c)
	}
	taskID, err := st.AddTask(r.id, 0, "setup", "system", "작업 폴더 준비", strings.Join(plan, "\n"))
	if err != nil {
		return err
	}
	st.SetTaskStatus(taskID, store.InProgress, "")
	var report []string
	fail := func(msg string) error {
		report = append(report, msg)
		st.SetTaskResult(taskID, 1, strings.Join(report, "\n"))
		st.SetTaskStatus(taskID, store.Failed, oneLine(msg, 200))
		r.o.log("system", "setup failed: "+oneLine(msg, 200))
		return errors.New(msg)
	}

	copied, missing, err := workspace.CopyIgnored(r.wt.Repo, r.wt.Dir, r.cfg.CopyFiles)
	if err != nil {
		return fail(err.Error())
	}
	if len(copied) > 0 {
		report = append(report, "복사함: "+strings.Join(copied, ", "))
		r.o.log("system", "copied into work folder: "+strings.Join(copied, ", "))
	}
	if len(missing) > 0 {
		report = append(report, "프로젝트 폴더에 없어서 건너뜀: "+strings.Join(missing, ", "))
		r.o.log("system", "not in project folder, skipped: "+strings.Join(missing, ", "))
	}

	timeout := r.cfg.SetupTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	for i, cmd := range r.cfg.Setup {
		logPath := filepath.Join(r.dir, fmt.Sprintf("setup-%d.txt", i+1))
		res := check.Run(ctx, r.wt.Dir, cmd, timeout, logPath)
		if ctx.Err() != nil {
			return fail("중지됨")
		}
		if !res.Passed() {
			why := fmt.Sprintf("exit %d", res.ExitCode)
			if res.TimedOut {
				why = fmt.Sprintf("시간 초과 %s", timeout)
			}
			return fail(fmt.Sprintf("준비 명령 `%s` 실패(%s):\n%s", cmd, why, check.Tail(res.Output, 2000)))
		}
		report = append(report, fmt.Sprintf("실행함(%.1f초): %s", res.Duration.Seconds(), cmd))
		r.o.log("system", fmt.Sprintf("setup PASS (%.1fs): %s", res.Duration.Seconds(), cmd))
	}

	changes, err := workspace.Status(r.wt.Dir)
	if err != nil {
		return fail(err.Error())
	}
	if len(changes) > 0 {
		var paths []string
		for _, c := range changes {
			paths = append(paths, c.Path)
		}
		return fail("준비 명령이 git에 잡히는 파일을 만들거나 바꿨습니다: " + strings.Join(paths, ", ") +
			". AI의 변경 내용에 섞이므로 .gitignore에 넣거나 명령을 고치세요")
	}
	st.SetTaskResult(taskID, 1, strings.Join(report, "\n"))
	st.SetTaskStatus(taskID, store.Completed, "")
	return nil
}

// answer runs a read-only mode: one employee reads the project and writes
// an answer, a plan or a review. Nothing in the project changes.
func (r *run) answer(ctx context.Context) (string, string) {
	role, title, system := "", "", ""
	switch r.cfg.Mode {
	case ModeAsk:
		role, title, system = "ask", "질문 답변", askSystemPrompt()
	case ModePlan:
		role, title, system = "pm", "기획", planDocSystemPrompt()
	case ModeAudit:
		role, title, system = "qa", "코드 검토", auditSystemPrompt()
	case ModeLearn:
		return r.learn(ctx)
	}
	if r.cfg.Mode == ModeMeeting {
		panel := r.cfg.Panels["meeting"]
		if len(panel) < 2 {
			return store.ReqFailed, "회의에는 2명 이상이 필요합니다"
		}
		md, err := r.discussion(ctx, panel, "결정할 문제:\n"+r.cfg.Request)
		if err != nil {
			return store.ReqFailed, err.Error()
		}
		return r.saveAnswer("pm", "회의 결론", md)
	}
	if panel := r.cfg.Panels["plan"]; r.cfg.Mode == ModePlan && len(panel) >= 2 {
		res, note, err := r.draftCritique(ctx, panel, "기획 회의", "사용자 요청:\n"+r.cfg.Request, planDocSystemPrompt(), "")
		if err != nil {
			return store.ReqFailed, err.Error()
		}
		text := res.Text
		if note != "" {
			text = "> " + note + "\n\n" + text
		}
		status, reason := r.saveAnswer("pm", "기획서(회의)", text)
		if note != "" && reason == "" {
			reason = note
		}
		return status, reason
	}
	if panel := r.cfg.Panels["review"]; r.cfg.Mode == ModeAudit && len(panel) >= 1 {
		cr, err := r.crossReview(ctx, panel, "교차 검토", "사용자 요청(검토 범위):\n"+r.cfg.Request, auditPanelSystemPrompt())
		if err != nil {
			return store.ReqFailed, err.Error()
		}
		md := cr.markdown(panel)
		taskID, err := r.o.Store.AddTask(r.id, 300, "answer", "qa", "교차 검토 결과", r.cfg.Request)
		if err != nil {
			return store.ReqFailed, err.Error()
		}
		r.o.Store.SetTaskResult(taskID, 0, md)
		r.o.Store.SetTaskStatus(taskID, store.Completed, "")
		os.WriteFile(filepath.Join(r.dir, "answer.md"), []byte(md), 0o644)
		return store.ReqDone, ""
	}
	model := r.cfg.DevModel
	if role == "pm" && r.cfg.PMModel != "" {
		model = r.cfg.PMModel
	}
	taskID, err := r.o.Store.AddTask(r.id, 1, "answer", role, title, r.cfg.Request)
	if err != nil {
		return store.ReqFailed, err.Error()
	}
	res, err := r.runTask(ctx, taskID, role, model, "사용자 요청:\n"+r.cfg.Request, system, "")
	if err != nil {
		return store.ReqFailed, err.Error()
	}
	r.o.Store.SetTaskStatus(taskID, store.Completed, "")
	os.WriteFile(filepath.Join(r.dir, "answer.md"), []byte(res.Text), 0o644)
	r.o.log(role, fmt.Sprintf("answer written (%d chars)", len([]rune(res.Text))))
	return store.ReqDone, ""
}

// learn has one employee read the project and draft its knowledge document.
// The draft is the request's answer; the person reviews it before it is
// saved to the repository. The human section of the current document is
// carried over by code, whatever the AI wrote.
func (r *run) learn(ctx context.Context) (string, string) {
	taskID, err := r.o.Store.AddTask(r.id, 1, "answer", "ask", "프로젝트 파악", r.cfg.Request)
	if err != nil {
		return store.ReqFailed, err.Error()
	}
	res, err := r.runTask(ctx, taskID, "ask", r.cfg.DevModel, learnPrompt(r.cfg.Request, r.cfg.Knowledge), learnSystemPrompt(), "")
	if err != nil {
		return store.ReqFailed, err.Error()
	}
	if strings.TrimSpace(res.Text) == "" {
		r.o.Store.SetTaskStatus(taskID, store.Failed, "empty document")
		return store.ReqFailed, "AI가 빈 문서를 냈습니다"
	}
	doc := knowledge.Merge(res.Text, r.cfg.Knowledge)
	r.o.Store.SetTaskResult(taskID, 1, doc)
	r.o.Store.SetTaskStatus(taskID, store.Completed, "")
	os.WriteFile(filepath.Join(r.dir, "answer.md"), []byte(doc), 0o644)
	r.o.log("ask", fmt.Sprintf("knowledge draft written (%d chars)", len([]rune(doc))))
	return store.ReqDone, ""
}

// saveAnswer records the result of a read-only meeting as the request's answer.
func (r *run) saveAnswer(role, title, text string) (string, string) {
	taskID, err := r.o.Store.AddTask(r.id, 300, "answer", role, title, r.cfg.Request)
	if err != nil {
		return store.ReqFailed, err.Error()
	}
	r.o.Store.SetTaskResult(taskID, 0, text)
	r.o.Store.SetTaskStatus(taskID, store.Completed, "")
	os.WriteFile(filepath.Join(r.dir, "answer.md"), []byte(text), 0o644)
	return store.ReqDone, ""
}

// maxReviewDiff caps the diff sent to the reviewer; it can read files itself.
const maxReviewDiff = 40000

// review asks the QA agent to judge the diff against the request.
// Returns "" when approved, otherwise the issues for the developer.
func (r *run) review(ctx context.Context, round int) (string, error) {
	diff, err := r.wt.Diff()
	if err != nil {
		return "", err
	}
	if len(diff) > maxReviewDiff {
		diff = diff[:maxReviewDiff] + "\n... (diff truncated; read the files for the rest)"
	}
	model := r.cfg.QAModel
	if model == "" {
		model = r.cfg.DevModel
	}
	taskID, err := r.o.Store.AddTask(r.id, 100+round, "review", "qa", fmt.Sprintf("QA 리뷰 %d회차", round+1), "")
	if err != nil {
		return "", err
	}
	res, err := r.runTask(ctx, taskID, "qa", model, reviewPrompt(r.cfg.Request, diff), reviewSystemPrompt(r.cfg.Checks, r.cfg.Protect), reviewSchema)
	if err != nil {
		return "", err
	}
	var rv review
	if err := decodeAnswer(res, &rv); err != nil {
		r.o.Store.SetTaskStatus(taskID, store.Failed, "review was not valid JSON")
		return "", fmt.Errorf("reviewer did not return a valid review: %v", err)
	}
	if rv.Approved && len(rv.Issues) == 0 {
		r.o.Store.SetTaskStatus(taskID, store.Completed, "approved")
		r.o.log("qa", "review APPROVED: "+oneLine(rv.Summary, 140))
		return "", nil
	}
	r.o.Store.SetTaskStatus(taskID, store.Completed, fmt.Sprintf("%d issue(s)", len(rv.Issues)))
	r.o.log("qa", fmt.Sprintf("review: %d issue(s): %s", len(rv.Issues), oneLine(rv.Summary, 140)))
	for _, is := range rv.Issues {
		r.o.log("qa", "  - "+is.File+": "+oneLine(is.Problem, 120))
	}
	return rv.failures(), nil
}

// commit records the team's work on the request branch so it can be
// reviewed with git and merged only when the user approves.
func (r *run) commit() error {
	changes, err := workspace.Status(r.wt.Dir)
	if err != nil || len(changes) == 0 {
		return err
	}
	return r.wt.Commit("AI Office: " + oneLine(r.cfg.Request, 60))
}

func rel(dir, p string) string {
	if p == "" || !filepath.IsAbs(p) {
		return p
	}
	if r, err := filepath.Rel(dir, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return p
}
