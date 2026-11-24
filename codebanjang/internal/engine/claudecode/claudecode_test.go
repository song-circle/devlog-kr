package claudecode

import (
	"bytes"
	"os"
	"slices"
	"testing"

	"github.com/song-circle/devlog-kr/codebanjang/internal/engine"
)

func TestParseStream(t *testing.T) {
	f, err := os.Open("testdata/edit.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var res engine.Result
	var kinds []engine.EventKind
	var targets []string
	got, err := parseStream(f, func(ev engine.Event) {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == engine.EventToolUse {
			targets = append(targets, ev.Target)
		}
	}, &res)
	if err != nil || !got {
		t.Fatalf("parseStream: got=%v err=%v", got, err)
	}
	want := []engine.EventKind{engine.EventInit, engine.EventText, engine.EventToolUse, engine.EventToolUse, engine.EventToolError}
	if !slices.Equal(kinds, want) {
		t.Errorf("events = %v, want %v", kinds, want)
	}
	if !slices.Equal(targets, []string{"C:/w/a.txt", "C:/outside/x.txt"}) {
		t.Errorf("targets = %v", targets)
	}
	if res.Status != engine.StatusSuccess || res.Turns != 3 || res.ToolCalls != 2 || res.ToolErrors != 1 || res.Denials != 1 {
		t.Errorf("result = %+v", res)
	}
	if res.Usage.CostUSD != 0.0163408 || res.Usage.OutputTokens != 197 || res.Usage.CacheReadTokens != 16619 {
		t.Errorf("usage = %+v", res.Usage)
	}
}

func TestParseStreamNoResult(t *testing.T) {
	// Cut the stream before the result line: a crash mid-run.
	b, _ := os.ReadFile("testdata/edit.jsonl")
	lines := 0
	cut := 0
	for i, c := range b {
		if c == '\n' {
			lines++
			if lines == 8 {
				cut = i + 1
			}
		}
	}
	var res engine.Result
	got, err := parseStream(bytes.NewReader(b[:cut]), nil, &res)
	if err != nil || got {
		t.Fatalf("got=%v err=%v, want no result", got, err)
	}
}

// A run killed before its result message keeps per-response usage.
func TestParseStreamKilledUsage(t *testing.T) {
	f, err := os.Open("testdata/killed.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var res engine.Result
	got, err := parseStream(f, nil, &res)
	if err != nil || got {
		t.Fatalf("got=%v err=%v, want no result", got, err)
	}
	u := res.Usage
	if !u.CostEstimated {
		t.Error("CostEstimated = false")
	}
	if u.InputTokens != 14 || u.OutputTokens != 91+116+1 || u.CacheReadTokens != 4483+6495 || u.CacheCreateTokens != 2012+320 {
		t.Errorf("usage = %+v", u)
	}
	// Sonnet 5: in $2, out $10, cache read $0.20, 1h cache write $4 per 1M.
	want := (4*2.0 + 207*10.0 + 10978*0.20 + 2332*4.0) / 1e6
	if diff := u.CostUSD - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("cost = %.7f, want %.7f", u.CostUSD, want)
	}
	if len(u.UnpricedModels) != 1 || u.UnpricedModels[0] != "claude-unknown-9" {
		t.Errorf("unpriced = %v", u.UnpricedModels)
	}
}

// The engine's own total for a finished run (summary of run 20261118-164507)
// matches our price table: Sonnet 5 part + a small Haiku 4.5 side call.
func TestPriceMatchesEngineTotal(t *testing.T) {
	sonnet, _ := engine.MessageUsage{Model: "claude-sonnet-5", Input: 10, Output: 2311, CacheRead: 51314, CacheWrite1h: 4556}.Cost()
	haiku, _ := engine.MessageUsage{Model: "claude-haiku-4-5-20251001", Input: 1000, Output: 25}.Cost()
	if got, engineTotal := sonnet+haiku, 0.0526498; got < engineTotal*0.99 || got > engineTotal*1.01 {
		t.Errorf("priced %.7f, engine reported %.7f", got, engineTotal)
	}
}

func TestArgsDefaults(t *testing.T) {
	a := Args(engine.Spec{Model: "sonnet", MaxBudgetUSD: 0.5})
	for _, must := range []string{"-p", "--restricted", "--safe-mode", "--no-session-persistence"} {
		if !slices.Contains(a, must) {
			t.Errorf("args missing %s: %v", must, a)
		}
	}
	i := slices.Index(a, "--tools")
	if i < 0 || a[i+1] != "Read,Edit,Write,Glob,Grep" {
		t.Errorf("default tools wrong: %v", a)
	}
	i = slices.Index(a, "--max-budget-usd")
	if i < 0 || a[i+1] != "0.50" {
		t.Errorf("budget wrong: %v", a)
	}
}

// A usage-limit stop is its own status, with the engine's message kept.
func TestParseStreamLimited(t *testing.T) {
	f, err := os.Open("testdata/limited.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var res engine.Result
	if _, err := parseStream(f, nil, &res); err != nil {
		t.Fatal(err)
	}
	if res.Status != engine.StatusLimited || res.Text == "" {
		t.Errorf("status %q text %q", res.Status, res.Text)
	}
}
