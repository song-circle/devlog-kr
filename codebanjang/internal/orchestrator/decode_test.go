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

func TestDecodeAnswer(t *testing.T) {
	var c critique
	// Structured output (large models).
	if err := decodeAnswer(engine.Result{Structured: []byte(`{"approve":true,"summary":"ok","points":[]}`)}, &c); err != nil || !c.Approve {
		t.Errorf("structured: %v %+v", err, c)
	}
	// JSON written into the text, with talk and a code fence around it.
	c = critique{}
	text := "검토했습니다.\n```json\n{\"approve\": false, \"summary\": \"근거 {부족}\", \"points\": [{\"point\":\"a\",\"why\":\"b\",\"suggestion\":\"c\"}]}\n```\n끝."
	if err := decodeAnswer(engine.Result{Text: text}, &c); err != nil || c.Approve || len(c.Points) != 1 {
		t.Errorf("embedded: %v %+v", err, c)
	}
	// What granite3.3:8b wrote in bug 014: no JSON at all.
	if err := decodeAnswer(engine.Result{Text: `<StructuredOutput approve={true} summary="Reviewing" />`}, &c); err == nil {
		t.Error("pseudo-XML accepted")
	}
	if err := decodeAnswer(engine.Result{}, &c); err == nil {
		t.Error("empty answer accepted")
	}
}

// badCriticEngine: critic "B" never answers in the format; "C" approves.
type badCriticEngine struct {
	mu    sync.Mutex
	calls int
}

func (e *badCriticEngine) Name() string { return "bad-critic" }

func (e *badCriticEngine) Run(ctx context.Context, s engine.Spec, on func(engine.Event)) (engine.Result, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()
	res := engine.Result{Status: engine.StatusSuccess}
	switch {
	case strings.Contains(s.JSONSchema, `"approve"`) && s.Model == "B":
		res.Text = `<StructuredOutput approve={true} />`
	case strings.Contains(s.JSONSchema, `"approve"`):
		res.Structured, _ = json.Marshal(map[string]any{"approve": true, "summary": "좋음", "points": []any{}})
	default:
		res.Text = "초안"
	}
	return res, nil
}

// One critic answering in the wrong format is left out of that round; the
// meeting still finishes with the others' approval (bug 014).
func TestMeetingSkipsBadMember(t *testing.T) {
	repo := newRepo(t)
	data := t.TempDir()
	st, _ := store.Open(filepath.Join(data, "aio.db"))
	defer st.Close()
	o := &Orchestrator{Engine: &badCriticEngine{}, Store: st, DataDir: data}
	panel := []Agent{{Name: "PM", Model: "A"}, {Name: "그래닛", Model: "B"}, {Name: "큐웬", Model: "C"}}
	out, err := o.Run(context.Background(), Config{Repo: repo, Request: "x", Mode: ModePlan, BudgetUSD: 5,
		Panels: map[string][]Agent{"plan": panel}})
	if err != nil || out.Status != store.ReqDone || out.Reason != "" {
		t.Fatalf("%v %+v", err, out)
	}
	msgs, _ := st.Messages(out.RequestID)
	skipped := 0
	for _, m := range msgs {
		if m.Kind == "skipped" && m.Sender == "그래닛" {
			skipped++
		}
	}
	if skipped != 1 {
		t.Errorf("skipped messages for the bad critic = %d, want 1", skipped)
	}
}

// Local employees run one at a time.
func TestLocalAgentsTakeTurns(t *testing.T) {
	repo := newRepo(t)
	data := t.TempDir()
	st, _ := store.Open(filepath.Join(data, "aio.db"))
	defer st.Close()
	ce := &concurrencyEngine{}
	o := &Orchestrator{Engine: ce, Store: st, DataDir: data}
	panel := []Agent{{Name: "가", Model: "A", Local: true}, {Name: "나", Model: "B", Local: true}, {Name: "다", Model: "C", Local: true}}
	if _, err := o.Run(context.Background(), Config{Repo: repo, Request: "x", Mode: ModeMeeting, BudgetUSD: 5,
		Panels: map[string][]Agent{"meeting": panel}}); err != nil {
		t.Fatal(err)
	}
	if ce.max != 1 {
		t.Errorf("local runs at the same time: %d, want 1", ce.max)
	}
}

type concurrencyEngine struct {
	mu       sync.Mutex
	now, max int
}

func (e *concurrencyEngine) Name() string { return "conc" }

func (e *concurrencyEngine) Run(ctx context.Context, s engine.Spec, on func(engine.Event)) (engine.Result, error) {
	e.mu.Lock()
	e.now++
	if e.now > e.max {
		e.max = e.now
	}
	e.mu.Unlock()
	defer func() { e.mu.Lock(); e.now--; e.mu.Unlock() }()
	res := engine.Result{Status: engine.StatusSuccess}
	if strings.Contains(s.JSONSchema, `"position"`) {
		res.Structured, _ = json.Marshal(map[string]any{"position": "p", "reasons": []string{}, "changed": false, "agree_with": []string{}, "disagree": []any{}})
	} else {
		res.Text = "결론"
	}
	for i := 0; i < 3e6; i++ { // give overlapping runs a chance to overlap
	}
	return res, nil
}
