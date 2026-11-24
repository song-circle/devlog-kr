package orchestrator

// Meetings: several employees work on one question under fixed rules.
//
// Cross-review (this file): every reviewer looks at the same thing on their
// own, then every finding is checked by the other reviewers. A finding is
// kept only when a majority of the panel (its author included) says it is
// real. The tally is mechanical; no agent decides the outcome alone.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/song-circle/devlog-kr/codebanjang/internal/store"
)

const findingsSchema = `{"type":"object","properties":{` +
	`"summary":{"type":"string"},` +
	`"issues":{"type":"array","items":{"type":"object","properties":{` +
	`"file":{"type":"string"},"severity":{"type":"string","enum":["high","medium","low"]},` +
	`"problem":{"type":"string"},"evidence":{"type":"string"},"fix":{"type":"string"}` +
	`},"required":["file","severity","problem","evidence","fix"]}}` +
	`},"required":["summary","issues"]}`

const verdictsSchema = `{"type":"object","properties":{` +
	`"verdicts":{"type":"array","items":{"type":"object","properties":{` +
	`"id":{"type":"string"},"verdict":{"type":"string","enum":["valid","invalid","unsure"]},"reason":{"type":"string"},` +
	`"evidence":{"type":"string"}` +
	`},"required":["id","verdict","reason","evidence"]}}` +
	`},"required":["verdicts"]}`

// Finding is one issue raised in a cross-review, with the panel's votes.
type Finding struct {
	ID       string    `json:"id"`
	Author   string    `json:"author"`
	File     string    `json:"file"`
	Severity string    `json:"severity"`
	Problem  string    `json:"problem"`
	Evidence string    `json:"evidence"`
	Fix      string    `json:"fix"`
	Votes    []Verdict `json:"votes"`
	Accepted bool      `json:"accepted"`
}

// Verdict is one reviewer's judgement of another reviewer's finding.
type Verdict struct {
	ID       string `json:"id"`
	Voter    string `json:"voter,omitempty"`
	Verdict  string `json:"verdict"` // valid / invalid / unsure
	Reason   string `json:"reason"`
	Evidence string `json:"evidence,omitempty"` // what the voter checked (file:line, spec text)
	Demoted  bool   `json:"demoted,omitempty"`  // "valid" without evidence, counted as unsure
}

// hasEvidence reports whether a verdict points at something concrete: a
// file location or a quoted line, not just agreement.
func hasEvidence(e string) bool {
	e = strings.TrimSpace(e)
	return len([]rune(e)) >= 8 && strings.ContainsAny(e, ":.\"'`(")
}

type findings struct {
	Summary string `json:"summary"`
	Issues  []struct {
		File, Severity, Problem, Evidence, Fix string
	} `json:"issues"`
}

// CrossReviewResult is the outcome of a cross-review meeting.
type CrossReviewResult struct {
	Findings []Finding
	Summary  map[string]string // reviewer -> own summary
}

// Accepted returns the findings the majority confirmed.
func (c *CrossReviewResult) Accepted() []Finding {
	var out []Finding
	for _, f := range c.Findings {
		if f.Accepted {
			out = append(out, f)
		}
	}
	return out
}

// tally marks findings accepted when more than half of the panel (the
// author counts as one yes) says valid. "unsure" is not a yes.
func tally(fs []Finding, panelSize int) {
	for i := range fs {
		yes := 1
		for _, v := range fs[i].Votes {
			if v.Verdict == "valid" {
				yes++
			}
		}
		// Majority of those who looked at it (author + voters). A finding
		// nobody else checked is not confirmed.
		n := 1 + len(fs[i].Votes)
		fs[i].Accepted = len(fs[i].Votes) > 0 && yes*2 > n
	}
}

// parallel runs fn for every panel member at once and returns the first error.
func parallel(panel []Agent, fn func(i int, ag Agent) error) error {
	var wg sync.WaitGroup
	errs := make([]error, len(panel))
	for i, ag := range panel {
		wg.Add(1)
		go func(i int, ag Agent) {
			defer wg.Done()
			errs[i] = fn(i, ag)
		}(i, ag)
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// crossReview holds a cross-review meeting about subject (the request plus
// what to review). find is the reviewers' role instructions.
func (r *run) crossReview(ctx context.Context, panel []Agent, title, subject, find string) (*CrossReviewResult, error) {
	st := r.o.Store
	taskID, err := st.AddTask(r.id, 200+len(r.meetings), "meeting", "qa", title, subject)
	if err != nil {
		return nil, err
	}
	r.meetings = append(r.meetings, taskID)
	st.SetTaskStatus(taskID, store.Assigned, fmt.Sprintf("%d명", len(panel)))
	st.SetTaskStatus(taskID, store.InProgress, "")
	names := make([]string, len(panel))
	for i, ag := range panel {
		names[i] = ag.Name
	}
	r.o.log("meeting", fmt.Sprintf("%s: %s", title, strings.Join(names, ", ")))

	model := r.cfg.DevModel
	say := func(ag Agent, round int, kind, content string, data any, cost float64) {
		b, _ := json.Marshal(data)
		st.AddMessage(store.Message{RequestID: r.id, TaskID: taskID, Meeting: "cross-review", Round: round,
			Sender: ag.Name, Kind: kind, Content: content, DataJSON: string(b), CostUSD: cost})
	}

	// Round 1: everyone reviews on their own.
	own := make([]findings, len(panel))
	err = parallel(panel, func(i int, ag Agent) error {
		res, err := r.runAgent(ctx, taskID, "qa", ag, model, subject, find+crossFindRule, findingsSchema, false)
		if err == nil {
			err = decodeAnswer(res, &own[i])
		}
		if err != nil {
			return r.skipMember(taskID, "cross-review", 1, ag, err)
		}
		say(ag, 1, "finding", fmt.Sprintf("%s (지적 %d개)", own[i].Summary, len(own[i].Issues)), own[i], res.Usage.CostUSD)
		return nil
	})
	if err != nil {
		st.SetTaskStatus(taskID, store.Failed, err.Error())
		return nil, err
	}

	out := &CrossReviewResult{Summary: map[string]string{}}
	byAuthor := make([][]int, len(panel))
	for i, f := range own {
		out.Summary[panel[i].Name] = f.Summary
		for _, is := range f.Issues {
			byAuthor[i] = append(byAuthor[i], len(out.Findings))
			out.Findings = append(out.Findings, Finding{
				ID: fmt.Sprintf("F%d", len(out.Findings)+1), Author: panel[i].Name, File: is.File, Severity: is.Severity,
				Problem: is.Problem, Evidence: is.Evidence, Fix: is.Fix,
			})
		}
	}
	r.o.log("meeting", fmt.Sprintf("round 1: %d finding(s)", len(out.Findings)))

	// Round 2: everyone checks the others' findings.
	var mu sync.Mutex
	err = parallel(panel, func(i int, ag Agent) error {
		var theirs []Finding
		for j := range panel {
			if j == i {
				continue
			}
			for _, k := range byAuthor[j] {
				theirs = append(theirs, out.Findings[k])
			}
		}
		if len(theirs) == 0 {
			return nil
		}
		res, err := r.runAgent(ctx, taskID, "qa", ag, model, verifyPrompt(subject, theirs), find+crossVerifyRule, verdictsSchema, false)
		var vs struct {
			Verdicts []Verdict `json:"verdicts"`
		}
		if err == nil {
			err = decodeAnswer(res, &vs)
		}
		if err != nil {
			return r.skipMember(taskID, "cross-review", 2, ag, err)
		}
		valid := 0
		mu.Lock()
		for _, v := range vs.Verdicts {
			for k := range out.Findings {
				// Only count votes on others' findings, one per voter.
				if out.Findings[k].ID == v.ID && out.Findings[k].Author != ag.Name && !hasVoted(out.Findings[k], ag.Name) {
					v.Voter = ag.Name
					if v.Verdict == "valid" && !hasEvidence(v.Evidence) {
						// A bare "yes" is not a check (e004: a reviewer that found
						// nothing agreed with everything). It counts as unsure.
						v.Verdict, v.Demoted = "unsure", true
					}
					out.Findings[k].Votes = append(out.Findings[k].Votes, v)
					if v.Verdict == "valid" {
						valid++
					}
				}
			}
		}
		mu.Unlock()
		say(ag, 2, "verdict", fmt.Sprintf("다른 직원의 지적 %d개 중 %d개가 맞다고 판단", len(theirs), valid), vs, res.Usage.CostUSD)
		return nil
	})
	if err != nil {
		st.SetTaskStatus(taskID, store.Failed, err.Error())
		return nil, err
	}

	tally(out.Findings, len(panel))
	acc := len(out.Accepted())
	st.AddMessage(store.Message{RequestID: r.id, TaskID: taskID, Meeting: "cross-review", Round: 3, Sender: "코드반장",
		Kind: "conclusion", Content: fmt.Sprintf("과반이 확인한 지적 %d개 채택, %d개 기각", acc, len(out.Findings)-acc),
		DataJSON: mustJSON(out.Findings)})
	r.o.log("meeting", fmt.Sprintf("accepted %d / rejected %d", acc, len(out.Findings)-acc))
	st.SetTaskStatus(taskID, store.Completed, fmt.Sprintf("채택 %d, 기각 %d", acc, len(out.Findings)-acc))
	return out, nil
}

func hasVoted(f Finding, voter string) bool {
	for _, v := range f.Votes {
		if v.Voter == voter {
			return true
		}
	}
	return false
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

const crossFindRule = "\n\n이번 회의 규칙(1단계 · 각자 검토): 다른 직원도 같은 대상을 따로 검토하고, 네 지적은 다른 직원들이 사실인지 확인한다. " +
	"확실한 결함만 issues에 넣고, evidence에는 근거(파일:줄, 명세 문장, 반례 입력)를 구체적으로 쓴다. 결함이 없으면 issues를 비운다."

const crossVerifyRule = "\n\n이번 회의 규칙(2단계 · 교차 확인): 다른 직원들이 낸 지적이 실제 결함인지 하나씩 판정한다. " +
	"반드시 해당 파일을 직접 읽어 확인한다. 지적이 언어·라이브러리 동작에 대한 주장이면 그 주장이 사실인지 따진다. " +
	"맞으면 valid, 틀렸으면 invalid(왜 틀렸는지), 확인할 수 없으면 unsure. 모든 지적에 대해 id를 그대로 써서 판정한다. " +
	"evidence에는 네가 직접 확인한 근거(파일:줄과 그 줄의 내용, 명세 문장)를 쓴다. 근거 없이 valid라고 하면 unsure로 처리된다."

func verifyPrompt(subject string, fs []Finding) string {
	var b strings.Builder
	b.WriteString(subject)
	b.WriteString("\n\n확인할 지적(다른 직원들이 낸 것):\n")
	for _, f := range fs {
		fmt.Fprintf(&b, "- [%s] (%s, %s) %s\n  근거: %s\n  고칠 방법: %s\n", f.ID, f.File, f.Severity, f.Problem, f.Evidence, f.Fix)
	}
	return b.String()
}

// failuresFrom turns accepted findings into a fix request for the developer.
func failuresFrom(c *CrossReviewResult) string {
	acc := c.Accepted()
	if len(acc) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "QA 교차 검토에서 과반이 확인한 결함 %d개:\n", len(acc))
	for i, f := range acc {
		fmt.Fprintf(&b, "%d. [%s] %s\n   근거: %s\n   고칠 방법: %s\n", i+1, f.File, f.Problem, f.Evidence, f.Fix)
	}
	return b.String()
}

// markdown renders the meeting result for the audit answer.
func (c *CrossReviewResult) markdown(panel []Agent) string {
	var b strings.Builder
	names := make([]string, len(panel))
	for i, ag := range panel {
		names[i] = ag.Name
	}
	acc := c.Accepted()
	fmt.Fprintf(&b, "# 교차 검토 결과\n\n참여: %s · 지적 %d개 중 과반 확인 %d개 채택\n\n", strings.Join(names, ", "), len(c.Findings), len(acc))
	sev := map[string]int{"high": 0, "medium": 1, "low": 2}
	sorted := append([]Finding(nil), c.Findings...)
	sort.SliceStable(sorted, func(i, j int) bool { return sev[sorted[i].Severity] < sev[sorted[j].Severity] })
	for _, accepted := range []bool{true, false} {
		if accepted {
			b.WriteString("## 채택된 결함\n\n")
		} else {
			b.WriteString("## 기각된 지적 (과반이 확인하지 않음)\n\n")
		}
		n := 0
		for _, f := range sorted {
			if f.Accepted != accepted {
				continue
			}
			n++
			fmt.Fprintf(&b, "### %s · %s · %s (%s)\n%s\n\n- 근거: %s\n- 고칠 방법: %s\n", f.ID, f.Severity, f.File, f.Author, f.Problem, f.Evidence, f.Fix)
			for _, v := range f.Votes {
				fmt.Fprintf(&b, "- %s: **%s** — %s\n", v.Voter, v.Verdict, v.Reason)
			}
			b.WriteString("\n")
		}
		if n == 0 {
			b.WriteString("(없음)\n\n")
		}
	}
	b.WriteString("## 각자의 요약\n\n")
	for _, ag := range panel {
		fmt.Fprintf(&b, "- **%s**: %s\n", ag.Name, c.Summary[ag.Name])
	}
	return b.String()
}
