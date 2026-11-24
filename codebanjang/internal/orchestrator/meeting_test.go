package orchestrator

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/song-circle/devlog-kr/codebanjang/internal/engine"
	"github.com/song-circle/devlog-kr/codebanjang/internal/store"
)

// panelEngine answers as three reviewers told apart by model name:
// A reports a real defect, B reports a false one, C reports nothing.
// In the cross-check everyone confirms A's finding and rejects B's.
type panelEngine struct {
	mu    sync.Mutex
	calls []engine.Spec
}

func (p *panelEngine) Name() string { return "panel" }

func (p *panelEngine) Run(ctx context.Context, s engine.Spec, on func(engine.Event)) (engine.Result, error) {
	p.mu.Lock()
	p.calls = append(p.calls, s)
	p.mu.Unlock()
	res := engine.Result{Status: engine.StatusSuccess, Usage: engine.Usage{CostUSD: 0.01}}
	switch {
	case strings.Contains(s.JSONSchema, `"issues"`):
		f := map[string]any{"summary": s.Model + " 검토", "issues": []any{}}
		switch s.Model {
		case "A":
			f["issues"] = []any{map[string]string{"file": "a.go", "severity": "high", "problem": "큰 금액이 0이 됨", "evidence": "a.go:10", "fix": "오류 처리"}}
		case "B":
			f["issues"] = []any{map[string]string{"file": "a.go", "severity": "medium", "problem": "time.Parse가 2월 30일을 받아들임", "evidence": "추측", "fix": "검증 추가"}}
		}
		res.Structured, _ = json.Marshal(f)
	case strings.Contains(s.JSONSchema, `"verdicts"`):
		var vs []map[string]string
		for _, id := range []string{"F1", "F2"} {
			if !strings.Contains(s.Prompt, "["+id+"]") {
				continue // only judge what was shown
			}
			v := "valid"
			if strings.Contains(s.Prompt, "["+id+"] (a.go, medium)") {
				v = "invalid"
			}
			vs = append(vs, map[string]string{"id": id, "verdict": v, "reason": "확인함", "evidence": "a.go:10 확인"})
		}
		res.Structured, _ = json.Marshal(map[string]any{"verdicts": vs})
	}
	return res, nil
}

func TestCrossReviewAudit(t *testing.T) {
	repo := newRepo(t)
	data := t.TempDir()
	st, _ := store.Open(filepath.Join(data, "aio.db"))
	defer st.Close()
	pe := &panelEngine{}
	o := &Orchestrator{Engine: pe, Store: st, DataDir: data}
	panel := []Agent{{Name: "QA-A", Model: "A"}, {Name: "QA-B", Model: "B"}, {Name: "QA-C", Model: "C"}}
	out, err := o.Run(context.Background(), Config{Repo: repo, Request: "전체 검토", Mode: ModeAudit, BudgetUSD: 5,
		Panels: map[string][]Agent{"review": panel}})
	if err != nil || out.Status != store.ReqDone {
		t.Fatalf("%v %+v", err, out)
	}
	// 3 independent reviews + 3 cross-checks (each sees the others' findings).
	if len(pe.calls) != 6 {
		t.Errorf("engine calls = %d, want 6", len(pe.calls))
	}
	for _, c := range pe.calls {
		if strings.Join(c.Tools, ",") != "Read,Glob,Grep" {
			t.Errorf("reviewer got tools %v", c.Tools)
		}
	}
	tasks, _ := st.Tasks(out.RequestID)
	var answer string
	for _, tk := range tasks {
		if tk.Kind == "answer" {
			answer = tk.Result
		}
	}
	acc := answer[strings.Index(answer, "## 채택된 결함"):strings.Index(answer, "## 기각된 지적")]
	rej := answer[strings.Index(answer, "## 기각된 지적"):]
	if !strings.Contains(acc, "큰 금액이 0이 됨") || !strings.Contains(rej, "time.Parse가 2월 30일") {
		t.Errorf("answer:\n%s", answer)
	}
	msgs, _ := st.Messages(out.RequestID)
	kinds := map[string]int{}
	for _, m := range msgs {
		kinds[m.Kind]++
	}
	if kinds["finding"] != 3 || kinds["verdict"] != 3 || kinds["conclusion"] != 1 {
		t.Errorf("messages %v", kinds)
	}
}

func TestTallyMajority(t *testing.T) {
	fs := []Finding{
		{ID: "F1", Votes: []Verdict{{Verdict: "valid"}, {Verdict: "invalid"}}},  // 2 of 3
		{ID: "F2", Votes: []Verdict{{Verdict: "unsure"}, {Verdict: "invalid"}}}, // 1 of 3
		{ID: "F3", Votes: []Verdict{{Verdict: "valid"}}},                        // 2 of 2
		{ID: "F4", Votes: []Verdict{{Verdict: "unsure"}}},                       // 1 of 2
	}
	tally(fs[:2], 3)
	tally(fs[2:], 2)
	got := []bool{fs[0].Accepted, fs[1].Accepted, fs[2].Accepted, fs[3].Accepted}
	want := []bool{true, false, true, false}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s accepted=%v, want %v", fs[i].ID, got[i], want[i])
		}
	}
}

func TestBareYesIsNotEvidence(t *testing.T) {
	for e, want := range map[string]bool{"": false, "맞음": false, "동의합니다": false, "a.go:10 확인": true, "SPEC.md 14줄 \"1~2자리\"": true} {
		if got := hasEvidence(e); got != want {
			t.Errorf("hasEvidence(%q) = %v, want %v", e, got, want)
		}
	}
}
