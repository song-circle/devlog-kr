package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/song-circle/devlog-kr/codebanjang/internal/engine/claudecode"
	"github.com/song-circle/devlog-kr/codebanjang/internal/knowledge"
	"github.com/song-circle/devlog-kr/codebanjang/internal/orchestrator"
	"github.com/song-circle/devlog-kr/codebanjang/internal/store"
)

// rulesAgent is who reads finished requests for rules: a light model on
// the PC's default login (it only reads a short record).
var rulesAgent = orchestrator.Agent{Name: "규칙 정리", Model: "haiku"}

func (a *App) proposeRules(ctx context.Context, id string) (int, error) {
	r, err := a.st.GetRequest(id)
	if err != nil {
		return 0, err
	}
	if orchestrator.ReadOnly(r.Mode) || r.ProjectID == 0 {
		return 0, errors.New("코드 수정 요청에서만 규칙을 찾습니다")
	}
	doc, _ := knowledge.Load(r.Repo)
	start := time.Now()
	o := &orchestrator.Orchestrator{Engine: &claudecode.Engine{}, Store: a.st, DataDir: a.dataDir,
		Log: func(role, line string) {
			runtime.EventsEmit(a.ctx, "log", LogLine{ID: id, Seconds: time.Since(start).Seconds(), Role: role, Line: line})
		}}
	got, err := o.ProposeRules(ctx, r, doc, rulesAgent)
	return len(got), err
}

// ProposeRules reads a finished request (the person's follow-ups, failed
// checks, review findings) and stores rule candidates for its project.
// Returns how many were proposed.
func (a *App) ProposeRules(id string) (int, error) {
	if err := a.ready(); err != nil {
		return 0, err
	}
	return a.proposeRules(context.Background(), id)
}

// autoRules proposes rules once after a request in which someone had to
// step in. Errors are only logged: this is a side job.
func (a *App) autoRules(id string) {
	if a.st.HasRuleCandidates(id) {
		return
	}
	n, err := a.proposeRules(context.Background(), id)
	switch {
	case errors.Is(err, orchestrator.ErrNothingToLearn):
	case err != nil:
		runtime.EventsEmit(a.ctx, "log", LogLine{ID: id, Role: "system", Line: "규칙 찾기 실패: " + err.Error()})
	case n > 0:
		runtime.EventsEmit(a.ctx, "rules", map[string]any{"id": id, "count": n})
	}
}

// RuleCandidates lists the pending rule candidates of a folder.
func (a *App) RuleCandidates(projectID int64) ([]store.RuleCandidate, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	if !a.inWorkspace(projectID) {
		return nil, errors.New("이 작업공간의 폴더가 아닙니다")
	}
	return a.st.RuleCandidates(projectID, store.RulePending)
}

// DecideRule accepts (into the knowledge document, possibly with edited
// wording) or rejects a rule candidate.
func (a *App) DecideRule(id int64, accept bool, text string) error {
	if err := a.ready(); err != nil {
		return err
	}
	c, err := a.st.RuleCandidate(id)
	if err != nil {
		return err
	}
	if c.Status != store.RulePending {
		return fmt.Errorf("이미 %s 처리된 후보입니다", c.Status)
	}
	if !accept {
		return a.st.SetRuleStatus(id, store.RuleRejected, "")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		text = c.Rule
	}
	p, err := a.st.Project(c.ProjectID)
	if err != nil || !a.inWorkspace(c.ProjectID) {
		return errors.New("이 작업공간의 폴더가 아닙니다")
	}
	doc, err := knowledge.Load(p.Repo)
	if err != nil {
		return err
	}
	if strings.TrimSpace(doc) == "" {
		doc = knowledge.Merge("# "+p.Name+" 프로젝트 지식 문서\n", "")
	}
	if err := knowledge.Save(p.Repo, knowledge.AddRule(doc, text, "요청 "+c.RequestID)); err != nil {
		return err
	}
	return a.st.SetRuleStatus(id, store.RuleAccepted, text)
}
