package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/song-circle/devlog-kr/codebanjang/internal/engine"
	"github.com/song-circle/devlog-kr/codebanjang/internal/store"
)

// talkEngine scripts planning and discussion meetings. Critics approve only
// the second draft; positions name the speaker's model.
type talkEngine struct {
	mu     sync.Mutex
	calls  []engine.Spec
	drafts int
	failAt int // 1-based call number that runs out of budget (0 = never)
}

func (e *talkEngine) Name() string { return "talk" }

func (e *talkEngine) Run(ctx context.Context, s engine.Spec, on func(engine.Event)) (engine.Result, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, s)
	res := engine.Result{Status: engine.StatusSuccess, Usage: engine.Usage{CostUSD: 0.01}}
	if e.failAt == len(e.calls) {
		res.Status, res.Subtype = engine.StatusError, "error_max_budget_usd"
		return res, nil
	}
	switch {
	case strings.Contains(s.JSONSchema, `"approve"`):
		ok := strings.Contains(s.Prompt, "초안 v2")
		c := map[string]any{"approve": ok, "summary": "검토", "points": []any{}}
		if !ok {
			c["points"] = []any{map[string]string{"point": "완료 조건 없음", "why": "검증 불가", "suggestion": "추가"}}
		}
		res.Structured, _ = json.Marshal(c)
	case strings.Contains(s.JSONSchema, `"position"`):
		res.Structured, _ = json.Marshal(map[string]any{"position": s.Model + " 입장", "reasons": []string{"근거"},
			"changed": false, "agree_with": []string{}, "disagree": []any{}})
	case strings.Contains(s.SystemPrompt, "사회자"):
		res.Text = "결론: 둘 다 일리 있음"
	default: // a draft or a revision
		e.drafts++
		res.Text = fmt.Sprintf("초안 v%d", e.drafts)
	}
	return res, nil
}

func TestDraftCritiquePlan(t *testing.T) {
	repo := newRepo(t)
	data := t.TempDir()
	st, _ := store.Open(filepath.Join(data, "aio.db"))
	defer st.Close()
	te := &talkEngine{}
	o := &Orchestrator{Engine: te, Store: st, DataDir: data}
	panel := []Agent{{Name: "PM", Model: "A"}, {Name: "기획2", Model: "B"}, {Name: "기획3", Model: "C"}}
	out, err := o.Run(context.Background(), Config{Repo: repo, Request: "통계 화면", Mode: ModePlan, BudgetUSD: 5,
		Panels: map[string][]Agent{"plan": panel}})
	if err != nil || out.Status != store.ReqDone {
		t.Fatalf("%v %+v", err, out)
	}
	// draft, 2 critiques (reject), revision, 2 critiques (approve)
	if len(te.calls) != 6 {
		t.Errorf("engine calls = %d, want 6", len(te.calls))
	}
	tasks, _ := st.Tasks(out.RequestID)
	answer := ""
	for _, tk := range tasks {
		if tk.Kind == "answer" {
			answer = tk.Result
		}
	}
	if answer != "초안 v2" {
		t.Errorf("answer = %q, want the revised draft", answer)
	}
	msgs, _ := st.Messages(out.RequestID)
	kinds := map[string]int{}
	for _, m := range msgs {
		kinds[m.Kind]++
	}
	if kinds["draft"] != 1 || kinds["critique"] != 4 || kinds["revision"] != 1 {
		t.Errorf("messages %v", kinds)
	}
}

func TestDiscussionMeeting(t *testing.T) {
	repo := newRepo(t)
	data := t.TempDir()
	st, _ := store.Open(filepath.Join(data, "aio.db"))
	defer st.Close()
	te := &talkEngine{}
	o := &Orchestrator{Engine: te, Store: st, DataDir: data}
	panel := []Agent{{Name: "가", Model: "A"}, {Name: "나", Model: "B"}}
	out, err := o.Run(context.Background(), Config{Repo: repo, Request: "SQLite냐 파일이냐", Mode: ModeMeeting, BudgetUSD: 5,
		Panels: map[string][]Agent{"meeting": panel}})
	if err != nil || out.Status != store.ReqDone {
		t.Fatalf("%v %+v", err, out)
	}
	if len(te.calls) != 5 { // 2 members x 2 rounds + moderator
		t.Errorf("engine calls = %d, want 5", len(te.calls))
	}
	// In round 2 each member sees the other's position.
	saw := 0
	for _, c := range te.calls {
		if strings.Contains(c.JSONSchema, `"position"`) && strings.Contains(c.Prompt, "다른 참석자들의 입장") {
			if (c.Model == "A" && strings.Contains(c.Prompt, "B 입장")) || (c.Model == "B" && strings.Contains(c.Prompt, "A 입장")) {
				saw++
			}
		}
	}
	if saw != 2 {
		t.Errorf("round-2 prompts with the other's position: %d, want 2", saw)
	}
	tasks, _ := st.Tasks(out.RequestID)
	for _, tk := range tasks {
		if tk.Kind == "answer" && (!strings.Contains(tk.Result, "결론: 둘 다 일리 있음") || !strings.Contains(tk.Result, "회의 기록")) {
			t.Errorf("answer:\n%s", tk.Result)
		}
	}
	if _, err := o.Run(context.Background(), Config{Repo: repo, Request: "x", Mode: ModeMeeting, BudgetUSD: 5,
		Panels: map[string][]Agent{"meeting": panel[:1]}}); err != nil {
		t.Fatal(err)
	}
}

// A planning meeting that runs out of budget mid-way keeps its latest draft
// instead of failing with nothing (bug 013).
func TestDraftCritiqueKeepsDraftWhenStopped(t *testing.T) {
	repo := newRepo(t)
	data := t.TempDir()
	st, _ := store.Open(filepath.Join(data, "aio.db"))
	defer st.Close()
	te := &talkEngine{failAt: 4} // draft, 2 critiques, then the revision fails
	o := &Orchestrator{Engine: te, Store: st, DataDir: data}
	panel := []Agent{{Name: "PM", Model: "A"}, {Name: "기획2", Model: "B"}, {Name: "기획3", Model: "C"}}
	out, err := o.Run(context.Background(), Config{Repo: repo, Request: "통계 화면", Mode: ModePlan, BudgetUSD: 5,
		Panels: map[string][]Agent{"plan": panel}})
	if err != nil || out.Status != store.ReqDone {
		t.Fatalf("%v %+v", err, out)
	}
	if !strings.Contains(out.Reason, "멈췄습니다") {
		t.Errorf("reason %q", out.Reason)
	}
	tasks, _ := st.Tasks(out.RequestID)
	for _, tk := range tasks {
		if tk.Kind == "answer" && (!strings.Contains(tk.Result, "초안 v1") || !strings.Contains(tk.Result, "마지막 수정본")) {
			t.Errorf("answer:\n%s", tk.Result)
		}
	}
}

// When nobody changes position and nobody objects, the discussion ends
// instead of using the remaining rounds.
func TestDiscussionStopsWhenAgreed(t *testing.T) {
	repo := newRepo(t)
	data := t.TempDir()
	st, _ := store.Open(filepath.Join(data, "aio.db"))
	defer st.Close()
	te := &talkEngine{}
	o := &Orchestrator{Engine: te, Store: st, DataDir: data}
	panel := []Agent{{Name: "가", Model: "A"}, {Name: "나", Model: "B"}}
	out, err := o.Run(context.Background(), Config{Repo: repo, Request: "x", Mode: ModeMeeting, BudgetUSD: 5, MeetingRounds: 4,
		Panels: map[string][]Agent{"meeting": panel}})
	if err != nil || out.Status != store.ReqDone {
		t.Fatalf("%v %+v", err, out)
	}
	if len(te.calls) != 5 { // 2 rounds x 2 + moderator, not 4 rounds
		t.Errorf("engine calls = %d, want 5", len(te.calls))
	}
}
