package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/song-circle/devlog-kr/codebanjang/internal/engine/claudecode"
	"github.com/song-circle/devlog-kr/codebanjang/internal/knowledge"
	"github.com/song-circle/devlog-kr/codebanjang/internal/orchestrator"
	"github.com/song-circle/devlog-kr/codebanjang/internal/report"
	"github.com/song-circle/devlog-kr/codebanjang/internal/store"
	"github.com/song-circle/devlog-kr/codebanjang/internal/workspace"
)

// FollowUpInput continues a finished request.
type FollowUpInput struct {
	ID     string `json:"id"`
	Answer string `json:"answer"`
	// AllowProtected lets the developer change the project's protected
	// files this time (e.g. the tests, when the answer says so). The
	// knowledge document stays protected.
	AllowProtected bool    `json:"allowProtected"`
	BudgetUSD      float64 `json:"budgetUsd"` // more money for this follow-up (default 1)
}

// canFollowUp lists the states a request can be continued from.
var canFollowUp = map[string]bool{
	store.ReqDone: true, store.ReqNoChange: true, store.ReqFailed: true,
	store.ReqCancelled: true, store.ReqInterrupted: true,
}

// agentLike finds the employee who did a role before (by name), else the
// first employee with that role.
func (a *App) agentLike(name, role string) (orchestrator.Agent, error) {
	es, err := a.st.Employees()
	if err != nil {
		return orchestrator.Agent{}, err
	}
	var pick *store.Employee
	for i := range es {
		if es[i].Name == name && name != "" {
			pick = &es[i]
			break
		}
	}
	for i := range es {
		if pick == nil && es[i].Role == role {
			pick = &es[i]
		}
	}
	if pick == nil {
		return orchestrator.Agent{}, fmt.Errorf("역할이 %s 인 직원이 없습니다", role)
	}
	if pick.Connection == store.ConnOwn {
		if s := claudecode.Status(context.Background(), a.profileDir(pick.ID)); !s.LoggedIn {
			return orchestrator.Agent{}, fmt.Errorf("%s 직원이 로그인되어 있지 않습니다. 직원 화면에서 로그인하세요", pick.Name)
		}
	}
	return a.agentFor(*pick), nil
}

// FollowUp answers the developer's question, or asks for a change to the
// result, and continues the same request (same work folder and branch) in
// the background. Progress arrives as "log" events, the end as "done".
func (a *App) FollowUp(in FollowUpInput) error {
	if err := a.ready(); err != nil {
		return err
	}
	if in.Answer == "" {
		return errors.New("답이나 지시를 적어 주세요")
	}
	r, err := a.st.GetRequest(in.ID)
	if err != nil {
		return err
	}
	if readOnly(r) {
		return errors.New("읽기만 하는 요청은 이어서 시킬 수 없습니다. 새 요청으로 하세요")
	}
	if !canFollowUp[r.Status] {
		return fmt.Errorf("%s 상태의 요청은 이어서 시킬 수 없습니다(이미 반영했으면 새 요청으로)", r.Status)
	}
	if _, err := os.Stat(r.Worktree); err != nil {
		return errors.New("작업 폴더가 없어서 이어서 할 수 없습니다(폐기되었거나 정리됨). 새 요청으로 하세요")
	}
	proj, err := a.st.Project(r.ProjectID)
	if err != nil {
		return errors.New("요청의 프로젝트를 찾을 수 없습니다")
	}
	var cfg orchestrator.Config
	if err := json.Unmarshal([]byte(r.Config), &cfg); err != nil {
		return fmt.Errorf("요청 설정을 읽지 못했습니다: %w", err)
	}
	// Today's project settings, not the ones from when the request began.
	cfg.ID, cfg.Repo = r.ID, r.Repo
	cfg.Checks, cfg.CopyFiles, cfg.Setup = proj.Checks, proj.CopyFiles, proj.Setup
	cfg.Protect = append(append([]string{}, proj.Protect...), knowledge.RelPath)
	if in.AllowProtected {
		cfg.Protect = []string{knowledge.RelPath}
	}
	cfg.ProjectNotes = proj.Notes
	if cfg.Knowledge, err = knowledge.Load(r.Repo); err != nil {
		return err
	}
	cfg.Requester = requesterName(r.Repo)
	agents := map[string]orchestrator.Agent{}
	for _, role := range []string{"dev", "qa"} {
		if role == "qa" && cfg.Mode != orchestrator.ModeReview {
			continue
		}
		if agents[role], err = a.agentLike(cfg.Agents[role].Name, role); err != nil {
			return err
		}
	}
	cfg.Agents, cfg.Panels = agents, nil // a follow-up is one developer (plus QA review)
	if in.BudgetUSD <= 0 {
		in.BudgetUSD = 1
	}
	spent, _ := a.st.CostSoFar(r.ID)
	cfg.BudgetUSD = spent + in.BudgetUSD
	cfg.TaskTimeout, cfg.CheckTimeout = 15*time.Minute, 5*time.Minute

	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	if _, busy := a.running[r.ID]; busy {
		a.mu.Unlock()
		cancel()
		return errors.New("이미 진행 중입니다")
	}
	a.running[r.ID] = cancel
	a.mu.Unlock()
	start := time.Now()
	o := &orchestrator.Orchestrator{Engine: &claudecode.Engine{}, Store: a.st, DataDir: a.dataDir,
		Log: func(role, line string) {
			runtime.EventsEmit(a.ctx, "log", LogLine{ID: r.ID, Seconds: time.Since(start).Seconds(), Role: role, Line: line})
		}}
	wt := workspace.Open(r.Repo, r.Worktree, r.Branch, r.Base)
	go func() {
		defer func() {
			a.mu.Lock()
			delete(a.running, r.ID)
			a.mu.Unlock()
			cancel()
		}()
		out, err := o.FollowUp(ctx, cfg, r.ID, wt, in.Answer)
		ev := map[string]string{"id": r.ID}
		if err != nil {
			a.st.FinishRequest(r.ID, store.ReqFailed, err.Error())
			ev["status"], ev["reason"] = store.ReqFailed, err.Error()
		} else {
			ev["status"], ev["reason"] = out.Status, out.Reason
			diffStat, _ := wt.DiffStat()
			if md, err := report.Markdown(a.st.DB(), r.ID, diffStat); err == nil {
				os.WriteFile(filepath.Join(out.Dir, "report.md"), []byte(md), 0o644)
			}
		}
		runtime.EventsEmit(a.ctx, "done", ev)
		go a.autoRules(r.ID)
	}()
	return nil
}
