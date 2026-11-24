package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/song-circle/devlog-kr/codebanjang/internal/engine"
)

// skipMember handles one meeting member's failed statement. Problems that
// concern everyone (budget, usage limit, the user stopping) end the
// meeting; a bad answer or one member's timeout only drops that member for
// this round, and the meeting goes on.
func (r *run) skipMember(taskID int64, meeting string, round int, ag Agent, err error) error {
	msg := err.Error()
	if errors.Is(err, errBudget) || errors.Is(err, context.Canceled) ||
		strings.Contains(msg, "한도") || strings.Contains(msg, "예산") {
		return fmt.Errorf("%s: %w", ag.Name, err)
	}
	note := fmt.Sprintf("이번 라운드 발언 제외: %s", oneLine(msg, 160))
	r.note(taskID, meeting, round, ag.Name, "skipped", note, map[string]string{"error": msg}, 0)
	r.o.log("meeting", ag.Name+" — "+note)
	return nil
}

// decodeAnswer reads a structured answer. Large models return it through the
// engine's structured output; small local models often write the JSON into
// their text instead (or something JSON-like around it). The first JSON
// object in the text that decodes into v is accepted.
func decodeAnswer(res engine.Result, v any) error {
	if len(res.Structured) > 0 {
		if err := json.Unmarshal(res.Structured, v); err == nil {
			return nil
		}
	}
	text := res.Text
	for start := strings.IndexByte(text, '{'); start >= 0; {
		if obj := balancedObject(text[start:]); obj != "" {
			if json.Unmarshal([]byte(obj), v) == nil {
				return nil
			}
		}
		next := strings.IndexByte(text[start+1:], '{')
		if next < 0 {
			break
		}
		start += 1 + next
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("빈 답")
	}
	return errors.New("정해진 형식(JSON)으로 답하지 않음")
}

// balancedObject returns the {...} prefix of s with matching braces,
// skipping braces inside strings.
func balancedObject(s string) string {
	depth, inStr, esc := 0, false, false
	for i, c := range s {
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return s[:i+1]
			}
		}
	}
	return ""
}
