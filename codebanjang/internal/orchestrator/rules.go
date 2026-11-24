package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/song-circle/devlog-kr/codebanjang/internal/engine"
	"github.com/song-circle/devlog-kr/codebanjang/internal/knowledge"
	"github.com/song-circle/devlog-kr/codebanjang/internal/store"
)

// Learning from past requests: when a person had to step in (answer a
// question, ask for changes) or the checks and the reviewer sent work
// back, that usually reveals a rule of the project nobody wrote down.
// After such a request an employee reads what happened and proposes rules;
// a person accepts them into the knowledge document, so later requests
// start out knowing them.

const rulesSchema = `{"type":"object","properties":{"rules":{"type":"array","maxItems":3,"items":{"type":"object","properties":{` +
	`"rule":{"type":"string"},"why":{"type":"string"}},"required":["rule","why"]}}},"required":["rules"]}`

// ProposedRule is a rule an employee suggests for the project.
type ProposedRule struct {
	Rule string `json:"rule"`
	Why  string `json:"why"`
}

// ErrNothingToLearn means the request went through without anyone stepping in.
var ErrNothingToLearn = errors.New("배울 거리가 없습니다(사람의 추가 지시, 검사 실패, 리뷰 지적이 없음)")

// learningSignals collects what happened in a request that may hold a rule.
func learningSignals(tasks []store.TaskRow) (followUps, fixes []string) {
	for _, t := range tasks {
		switch t.Kind {
		case "followup":
			followUps = append(followUps, t.Instructions)
		case "fix":
			fixes = append(fixes, tail(t.Instructions, 1500))
		}
	}
	return
}

func tail(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return "..." + string(r[len(r)-n:])
	}
	return s
}

func rulesPrompt(r store.RequestRow, followUps, fixes []string, doc string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "아래는 이 프로젝트에서 처리한 요청 하나의 기록이다.\n\n요청:\n%s\n\n결과: %s %s\n", r.Text, r.Status, r.Reason)
	for i, f := range followUps {
		fmt.Fprintf(&b, "\n사람의 추가 지시 %d:\n%s\n", i+1, f)
	}
	for i, f := range fixes {
		fmt.Fprintf(&b, "\n검사 실패나 리뷰 지적으로 다시 고치게 한 내용 %d:\n%s\n", i+1, f)
	}
	if strings.TrimSpace(doc) != "" {
		fmt.Fprintf(&b, "\n지금 프로젝트 지식 문서(이미 있는 내용은 다시 제안하지 않는다):\n%s\n", knowledge.ForPrompt(doc))
	}
	return b.String()
}

const rulesSystem = "너는 이 프로젝트를 오래 맡아 온 개발자다. 요청 하나의 기록을 읽고, 앞으로 이 프로젝트의 다른 요청에서도 지켜야 할 규칙을 뽑는다.\n" +
	"규칙:\n" +
	"- 0~3개. 억지로 만들지 않는다. 배울 것이 없으면 빈 목록.\n" +
	"- 이번 요청에만 해당하는 지시(특정 버튼 이름, 이번 기능)는 규칙이 아니다. 다른 요청에도 적용되는 관례·제약·판단 기준만.\n" +
	"- 지식 문서에 이미 있는 것은 뺀다.\n" +
	"- rule은 '~한다' 형태의 한국어 한 문장, why는 이 기록에서 무슨 일이 있었는지 한 문장.\n" +
	"- 필요하면 저장소 파일을 읽어서 확인한다(읽기만 가능)."

// ProposeRules has an employee read a finished request and returns the
// rules it suggests (also stored as pending candidates for the project).
func (o *Orchestrator) ProposeRules(ctx context.Context, r store.RequestRow, doc string, ag Agent) ([]ProposedRule, error) {
	tasks, err := o.Store.Tasks(r.ID)
	if err != nil {
		return nil, err
	}
	followUps, fixes := learningSignals(tasks)
	if len(followUps) == 0 && len(fixes) == 0 {
		return nil, ErrNothingToLearn
	}
	model := ag.Model
	if model == "" {
		model = "haiku"
	}
	dir := r.Repo // read the project as it is
	logDir := filepath.Join(o.DataDir, "requests", r.ID, fmt.Sprintf("rules-%d", time.Now().Unix()))
	os.MkdirAll(logDir, 0o755)
	start := time.Now()
	res, runErr := o.Engine.Run(ctx, engine.Spec{
		Prompt: rulesPrompt(r, followUps, fixes, doc), Dir: dir, Model: model, Tools: []string{"Read", "Glob", "Grep"},
		MaxBudgetUSD: 0.5, Timeout: 5 * time.Minute, LogDir: logDir, SystemPrompt: rulesSystem, JSONSchema: rulesSchema,
		ProfileDir: ag.ProfileDir, Env: ag.Env,
	}, func(engine.Event) {})
	o.Store.AddRun(store.Run{RequestID: r.ID, Role: "rules", Engine: o.Engine.Name(), Model: model, Status: string(res.Status),
		Turns: res.Turns, ToolCalls: res.ToolCalls, ToolErrors: res.ToolErrors, Denials: res.Denials,
		InputTokens: res.Usage.InputTokens, OutputTokens: res.Usage.OutputTokens, CacheRead: res.Usage.CacheReadTokens, CacheWrite: res.Usage.CacheCreateTokens,
		CostUSD: res.Usage.CostUSD, CostEstimated: res.Usage.CostEstimated, Duration: time.Since(start), LogDir: logDir, ResultText: res.Text, StartedAt: start, Agent: ag.Name})
	if runErr != nil {
		return nil, runErr
	}
	if res.Status != engine.StatusSuccess {
		return nil, fmt.Errorf("규칙 찾기 실패: %s", res.Status)
	}
	var out struct {
		Rules []ProposedRule `json:"rules"`
	}
	if err := json.Unmarshal(res.Structured, &out); err != nil {
		return nil, fmt.Errorf("규칙 답을 읽지 못했습니다: %w", err)
	}
	var kept []ProposedRule
	for _, p := range out.Rules {
		p.Rule, p.Why = strings.TrimSpace(p.Rule), strings.TrimSpace(p.Why)
		if p.Rule == "" {
			continue
		}
		if err := o.Store.AddRuleCandidate(r.ProjectID, r.ID, p.Rule, p.Why); err != nil {
			return kept, err
		}
		kept = append(kept, p)
	}
	o.log("system", fmt.Sprintf("rule candidates: %d", len(kept)))
	return kept, nil
}
