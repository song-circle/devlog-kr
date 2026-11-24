package main

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

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/song-circle/devlog-kr/ai-office/internal/engine/claudecode"
	"github.com/song-circle/devlog-kr/ai-office/internal/knowledge"
	"github.com/song-circle/devlog-kr/ai-office/internal/ollama"
	"github.com/song-circle/devlog-kr/ai-office/internal/orchestrator"
	"github.com/song-circle/devlog-kr/ai-office/internal/report"
	"github.com/song-circle/devlog-kr/ai-office/internal/store"
	"github.com/song-circle/devlog-kr/ai-office/internal/workspace"
)

// App is bound to the frontend. Every method does real work on the local
// repository and database; nothing here is simulated.
type App struct {
	ctx     context.Context
	dataDir string
	st      *store.Store
	initErr error

	mu      sync.Mutex
	wsID    int64 // workspace open in this window (0 = start screen)
	running map[string]context.CancelFunc
	logins  map[int64]*claudecode.Login
}

func NewApp() *App {
	return &App{running: map[string]context.CancelFunc{}, logins: map[int64]*claudecode.Login{}}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.dataDir = filepath.Join(os.Getenv("LOCALAPPDATA"), "ai-office")
	if err := os.MkdirAll(a.dataDir, 0o755); err != nil {
		a.initErr = err
		return
	}
	st, err := store.Open(filepath.Join(a.dataDir, "aio.db"))
	if err != nil {
		a.initErr = err
		return
	}
	a.st = st
	st.MarkInterrupted() // requests left running by a previous session
	if err := st.SeedEmployees(); err != nil {
		a.initErr = err
	}
	a.migrateLastProject()
	a.migrateWorkspaces()
	a.setWorkspace(workspaceArg(os.Args[1:]))
}

func (a *App) shutdown(ctx context.Context) {
	a.mu.Lock()
	for _, cancel := range a.running {
		cancel()
	}
	for _, l := range a.logins {
		l.Cancel()
	}
	a.mu.Unlock()
	if a.st != nil {
		a.st.Close()
	}
}

func (a *App) ready() error {
	if a.initErr != nil {
		return a.initErr
	}
	if a.st == nil {
		return errors.New("database not open")
	}
	return nil
}

// ---- projects ----

// Projects lists the folders of this window's workspace.
func (a *App) Projects() ([]store.Project, error) {
	w, err := a.Current()
	if err != nil || w == nil {
		return []store.Project{}, err
	}
	return w.Projects, nil
}

// SaveProject updates the settings of a folder in this workspace (folders
// are added with AddFolder; the folder itself cannot change).
func (a *App) SaveProject(p store.Project) (int64, error) {
	if err := a.ready(); err != nil {
		return 0, err
	}
	old, err := a.st.Project(p.ID)
	if err != nil || !a.inWorkspace(p.ID) {
		return 0, errors.New("이 작업공간의 폴더가 아닙니다")
	}
	p.Repo = old.Repo
	return a.st.SaveProject(p)
}

// migrateLastProject turns the form values saved by earlier versions into
// a registered project, once.
func (a *App) migrateLastProject() {
	ps, err := a.st.Projects()
	if err != nil || len(ps) > 0 {
		return
	}
	v, _ := a.st.Setting("last_project")
	if v == "" {
		return
	}
	var old struct {
		Repo    string   `json:"repo"`
		Checks  []string `json:"checks"`
		Protect []string `json:"protect"`
	}
	if json.Unmarshal([]byte(v), &old) != nil || old.Repo == "" {
		return
	}
	a.st.SaveProject(store.Project{Name: filepath.Base(old.Repo), Repo: old.Repo, Checks: old.Checks, Protect: old.Protect})
}

// migrateWorkspaces gives every project registered before workspaces
// existed a workspace of its own, once, so it shows on the start screen.
func (a *App) migrateWorkspaces() {
	if ws, err := a.st.RecentWorkspaces(1); err != nil || len(ws) > 0 {
		return
	}
	if done, _ := a.st.Setting("workspaces_migrated"); done != "" {
		return
	}
	ps, _ := a.st.Projects()
	for _, p := range ps {
		a.st.SaveWorkspace(store.Workspace{Name: p.Name, ProjectIDs: []int64{p.ID}})
	}
	a.st.SetSetting("workspaces_migrated", "1")
}

// ---- requests ----

// StartInput is the new-request form.
type StartInput struct {
	ProjectID int64            `json:"projectId"`
	Request   string           `json:"request"`
	Mode      string           `json:"mode"` // ask / plan / audit / solo / team / review
	Team      map[string]int64 `json:"team"` // slot (ask/pm/dev/qa) -> employee id
	// Panels turn a stage into a meeting: "plan", "review" or "meeting" ->
	// employee ids in speaking order (the first one drafts / moderates).
	Panels    map[string][]int64 `json:"panels"`
	Rounds    int                `json:"rounds"`
	BudgetUSD float64            `json:"budgetUsd"`
	FixRounds int                `json:"fixRounds"`
}

// slotsFor lists who takes part in each mode.
func slotsFor(mode string) ([]string, error) {
	switch mode {
	case orchestrator.ModeAsk, orchestrator.ModeLearn:
		return []string{"ask"}, nil
	case orchestrator.ModePlan:
		return []string{"pm"}, nil
	case orchestrator.ModeAudit:
		return []string{"qa"}, nil
	case orchestrator.ModeSolo:
		return []string{"dev"}, nil
	case orchestrator.ModeTeam:
		return []string{"pm", "dev"}, nil
	case orchestrator.ModeReview:
		return []string{"pm", "dev", "qa"}, nil
	case orchestrator.ModeMeeting:
		return nil, nil
	}
	return nil, fmt.Errorf("알 수 없는 진행 방식: %s", mode)
}

// LogLine is pushed to the frontend as the "log" event.
type LogLine struct {
	ID      string  `json:"id"`
	Seconds float64 `json:"t"`
	Role    string  `json:"role"`
	Line    string  `json:"line"`
}

// Start begins a request in the background and returns its id at once.
// Progress arrives as "log" events; the end as a "done" event.
func (a *App) Start(in StartInput) (string, error) {
	if err := a.ready(); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Request) == "" {
		return "", errors.New("요청 내용을 적어 주세요")
	}
	proj, err := a.st.Project(in.ProjectID)
	if err != nil || !a.inWorkspace(proj.ID) {
		return "", errors.New("프로젝트를 골라 주세요")
	}
	top, err := workspace.TopLevel(proj.Repo)
	if err != nil {
		return "", fmt.Errorf("프로젝트 폴더가 git 저장소가 아닙니다: %s", proj.Repo)
	}
	a.st.SetSetting("last_project_id", fmt.Sprint(proj.ID))
	slots, err := slotsFor(in.Mode)
	if err != nil {
		return "", err
	}
	agentByID := func(id int64) (orchestrator.Agent, error) {
		e, err := a.st.Employee(id)
		if err != nil {
			return orchestrator.Agent{}, err
		}
		if e.Connection == store.ConnOwn {
			if s := claudecode.Status(context.Background(), a.profileDir(e.ID)); !s.LoggedIn {
				return orchestrator.Agent{}, fmt.Errorf("%s 직원이 로그인되어 있지 않습니다. 직원 화면에서 로그인하세요", e.Name)
			}
		}
		if e.Connection == store.ConnOllama {
			if s := ollamaStatus(ollama.Check(context.Background()), e.Model); !s.LoggedIn {
				return orchestrator.Agent{}, fmt.Errorf("%s 직원(Ollama)을 쓸 수 없습니다: %s", e.Name, s.Error)
			}
		}
		return a.agentFor(e), nil
	}
	agents := map[string]orchestrator.Agent{}
	for _, slot := range slots {
		id, ok := in.Team[slot]
		if !ok || id == 0 {
			return "", errors.New("참여할 직원을 골라 주세요")
		}
		ag, err := agentByID(id)
		if err != nil {
			return "", err
		}
		agents[slot] = ag
	}
	panels := map[string][]orchestrator.Agent{}
	for stage, ids := range in.Panels {
		if len(ids) == 0 {
			continue
		}
		if len(ids) < 2 {
			return "", errors.New("회의에는 2명 이상이 필요합니다")
		}
		if len(ids) > 4 {
			return "", errors.New("회의는 4명까지입니다")
		}
		for _, id := range ids {
			ag, err := agentByID(id)
			if err != nil {
				return "", err
			}
			panels[stage] = append(panels[stage], ag)
		}
	}
	if in.Mode == orchestrator.ModeMeeting && len(panels["meeting"]) < 2 {
		return "", errors.New("회의에 참석할 직원을 2명 이상 골라 주세요")
	}
	if in.BudgetUSD <= 0 {
		in.BudgetUSD = 2
	}
	doc, err := knowledge.Load(top)
	if err != nil {
		return "", fmt.Errorf("지식 문서를 읽지 못했습니다: %w", err)
	}
	// Code changes start from the latest shared development branch, where
	// they will be deployed, not from whatever this PC has checked out.
	baseRef := ""
	if _, target := a.deployTarget(proj.ID); target != "" && !orchestrator.ReadOnly(in.Mode) {
		if err := workspace.Fetch(top, remote, proj.DevBranch); err != nil {
			return "", fmt.Errorf("GitHub에서 %s 를 가져오지 못했습니다: %w", target, err)
		}
		baseRef = target
	}

	id := orchestrator.NewID()
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.running[id] = cancel
	a.mu.Unlock()

	start := time.Now()
	o := &orchestrator.Orchestrator{
		Engine:  &claudecode.Engine{},
		Store:   a.st,
		DataDir: a.dataDir,
		Log: func(role, line string) {
			runtime.EventsEmit(a.ctx, "log", LogLine{ID: id, Seconds: time.Since(start).Seconds(), Role: role, Line: line})
		},
	}
	cfg := orchestrator.Config{
		ID: id, Repo: top, Request: in.Request, Checks: proj.Checks, Protect: append(proj.Protect, knowledge.RelPath), // the document changes only through review
		PMModel: "sonnet", DevModel: "sonnet", Agents: agents, MaxFixRounds: in.FixRounds, BudgetUSD: in.BudgetUSD,
		TaskTimeout: 15 * time.Minute, CheckTimeout: 5 * time.Minute,
		BaseRef: baseRef, Requester: requesterName(top), CopyFiles: proj.CopyFiles, Setup: proj.Setup, SetupTimeout: 15 * time.Minute,
		Mode: in.Mode, ProjectID: proj.ID, ProjectNotes: proj.Notes, Knowledge: doc, Panels: panels, MeetingRounds: in.Rounds,
	}
	// Create the worktree and the database row now, so the request exists
	// (and setup errors reach the form) before this call returns.
	prep, err := o.Prepare(cfg)
	if err != nil {
		a.mu.Lock()
		delete(a.running, id)
		a.mu.Unlock()
		cancel()
		return "", err
	}
	go func() {
		defer func() {
			a.mu.Lock()
			delete(a.running, id)
			a.mu.Unlock()
			cancel()
		}()
		out, err := prep.Execute(ctx)
		ev := map[string]string{"id": id}
		if err != nil {
			ev["status"], ev["reason"] = store.ReqFailed, err.Error()
		} else {
			ev["status"], ev["reason"] = out.Status, out.Reason
			diffStat, _ := out.Worktree.DiffStat()
			if md, err := report.Markdown(a.st.DB(), id, diffStat); err == nil {
				os.WriteFile(filepath.Join(out.Dir, "report.md"), []byte(md), 0o644)
			}
		}
		runtime.EventsEmit(a.ctx, "done", ev)
	}()
	return id, nil
}

// Stop cancels a running request (its processes are killed).
func (a *App) Stop(id string) {
	a.mu.Lock()
	cancel := a.running[id]
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Running lists ids of requests running in this session.
func (a *App) Running() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	ids := make([]string, 0, len(a.running))
	for id := range a.running {
		ids = append(ids, id)
	}
	return ids
}

func (a *App) List() ([]store.RequestRow, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	w, err := a.Current()
	if err != nil || w == nil {
		return []store.RequestRow{}, err
	}
	repos := []string{}
	for _, p := range w.Projects {
		repos = append(repos, p.Repo)
	}
	return a.st.ListRequestsIn(repos, 200)
}

// Detail is everything the request screen shows.
type Detail struct {
	Request  store.RequestRow `json:"request"`
	Tasks    []store.TaskRow  `json:"tasks"`
	Checks   []store.CheckRow `json:"checks"`
	Report   string           `json:"report"`
	Diff     string           `json:"diff"`
	Answer   string           `json:"answer"`   // read-only modes: the answer / plan / review
	Messages []store.Message  `json:"messages"` // meeting statements
	// DeployTarget is where approving puts the change: "origin/dev" for a
	// project with a development branch, "" for the local folder.
	DeployTarget string `json:"deployTarget"`
}

// LastProjectID is the project used last time (0 if none).
func (a *App) LastProjectID() int64 {
	if a.ready() != nil {
		return 0
	}
	v, _ := a.st.Setting("last_project_id")
	var id int64
	fmt.Sscan(v, &id)
	return id
}

// readOnly reports whether a request never had its own branch.
func readOnly(r store.RequestRow) bool { return orchestrator.ReadOnly(r.Mode) || r.Branch == "" }

const maxDiff = 200000

func (a *App) Detail(id string) (*Detail, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	r, err := a.st.GetRequest(id)
	if err != nil {
		return nil, err
	}
	d := &Detail{Request: r}
	_, d.DeployTarget = a.deployTarget(r.ProjectID)
	if d.Messages, err = a.st.Messages(id); err != nil {
		return nil, err
	}
	if d.Tasks, err = a.st.Tasks(id); err != nil {
		return nil, err
	}
	if d.Checks, err = a.st.Checks(id); err != nil {
		return nil, err
	}
	diffStat := ""
	if readOnly(r) {
		for _, t := range d.Tasks {
			if t.Kind == "answer" {
				d.Answer = t.Result
			}
		}
		d.Report, _ = report.Markdown(a.st.DB(), id, "")
		return d, nil
	}
	switch r.Status {
	case store.ReqDone, store.ReqApproved:
		d.Diff, _ = workspace.DiffBranch(r.Repo, r.Base, r.Branch)
		diffStat, _ = workspace.Open(r.Repo, r.Worktree, r.Branch, r.Base).DiffStat()
	case store.ReqDev:
		// The branch now also holds others' changes merged in before the
		// push; the request's own change is its work commit.
		if work := a.st.WorkCommit(id); work != "" {
			d.Diff, _ = workspace.DiffBranch(r.Repo, r.Base, work)
		}
	case store.ReqRunning, store.ReqFailed, store.ReqCancelled, store.ReqInterrupted, store.ReqNoChange:
		if _, err := os.Stat(r.Worktree); err == nil {
			d.Diff, _ = workspace.Open(r.Repo, r.Worktree, r.Branch, r.Base).Diff()
		}
	}
	if len(d.Diff) > maxDiff {
		d.Diff = d.Diff[:maxDiff] + "\n... (잘림)"
	}
	d.Report, _ = report.Markdown(a.st.DB(), id, diffStat)
	return d, nil
}

// Approve merges the request branch into the branch checked out in the
// user's repository, then removes the worktree. Only DONE requests.
func (a *App) Approve(id string) error {
	if err := a.ready(); err != nil {
		return err
	}
	r, err := a.st.GetRequest(id)
	if err != nil {
		return err
	}
	if readOnly(r) {
		return errors.New("읽기만 한 요청이라 반영할 변경이 없습니다")
	}
	if r.Status != store.ReqDone {
		return fmt.Errorf("검증을 통과한 요청만 반영할 수 있습니다 (현재 %s)", r.Status)
	}
	// A branch still at its base has no work of its own: deploying it would
	// push nothing and record someone else's commit as this request's.
	if head := workspace.Resolve(r.Repo, r.Branch); head == "" || head == r.Base {
		return errors.New("이 요청에는 반영할 변경(커밋)이 없습니다")
	}
	if p, target := a.deployTarget(r.ProjectID); target != "" {
		return a.deployDev(r, p)
	}
	if err := workspace.MergeInto(r.Repo, r.Branch, "AI Office 요청 반영: "+oneLine(r.Text, 60)); err != nil {
		return fmt.Errorf("반영 실패(원래 상태로 되돌림): %w", err)
	}
	workspace.Open(r.Repo, r.Worktree, r.Branch, r.Base).Remove(false) // keep the branch for history
	return a.st.SetRequestStatus(id, store.ReqApproved, "")
}

// Discard removes the worktree and the branch of a finished request.
func (a *App) Discard(id string) error {
	if err := a.ready(); err != nil {
		return err
	}
	a.mu.Lock()
	_, busy := a.running[id]
	a.mu.Unlock()
	if busy {
		return errors.New("실행 중인 요청은 먼저 중지하세요")
	}
	r, err := a.st.GetRequest(id)
	if err != nil {
		return err
	}
	if readOnly(r) {
		// Never remove anything: the "worktree" of a read-only request is the project folder itself.
		return a.st.SetRequestStatus(id, store.ReqDiscarded, "")
	}
	if r.Status == store.ReqApproved || r.Status == store.ReqDiscarded || r.Status == store.ReqDev {
		return fmt.Errorf("이미 %s 상태입니다", r.Status)
	}
	if err := workspace.Open(r.Repo, r.Worktree, r.Branch, r.Base).Remove(true); err != nil {
		return err
	}
	return a.st.SetRequestStatus(id, store.ReqDiscarded, "")
}

func clean(list []string) []string {
	var out []string
	for _, s := range list {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
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
