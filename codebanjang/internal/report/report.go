// Package report turns what the store recorded for a request into a
// readable summary. Everything here is computed from the database only.
package report

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Markdown builds the report for one request.
func Markdown(db *sql.DB, reqID, diffStat string) (string, error) {
	var b strings.Builder
	var text, status, reason, branch, base, worktree, summary, created string
	var finished sql.NullString
	err := db.QueryRow(`SELECT text, status, reason, branch, base, worktree, plan_summary, created_at, finished_at
		FROM requests WHERE id=?`, reqID).Scan(&text, &status, &reason, &branch, &base, &worktree, &summary, &created, &finished)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&b, "# Request %s\n\n", reqID)
	fmt.Fprintf(&b, "- request: %s\n- status: **%s**", oneLine(text), status)
	if reason != "" {
		fmt.Fprintf(&b, " (%s)", reason)
	}
	fmt.Fprintf(&b, "\n- branch: `%s` (from %.8s)\n- worktree: `%s`\n", branch, base, worktree)
	if t0, err := time.Parse(time.RFC3339Nano, created); err == nil && finished.Valid {
		if t1, err := time.Parse(time.RFC3339Nano, finished.String); err == nil {
			fmt.Fprintf(&b, "- wall time: %.0fs\n", t1.Sub(t0).Seconds())
		}
	}
	if summary != "" {
		fmt.Fprintf(&b, "- plan: %s\n", summary)
	}

	// Tasks
	b.WriteString("\n## Tasks\n\n| # | kind | title | status | runs | cost | time |\n|---|---|---|---|---|---|---|\n")
	rows, err := db.Query(`SELECT t.seq, t.kind, t.title, t.status,
			COUNT(r.id), COALESCE(SUM(r.cost_usd),0), COALESCE(SUM(r.duration_ms),0), COALESCE(MAX(r.cost_estimated),0)
		FROM tasks t LEFT JOIN runs r ON r.task_id = t.id
		WHERE t.request_id=? GROUP BY t.id ORDER BY t.seq`, reqID)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var seq, runs, est int
		var kind, title, st string
		var cost float64
		var ms int64
		rows.Scan(&seq, &kind, &title, &st, &runs, &cost, &ms, &est)
		mark := ""
		if est != 0 {
			mark = "*"
		}
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %d | $%.4f%s | %.0fs |\n", seq, kind, oneLine(title), st, runs, cost, mark, float64(ms)/1000)
	}
	rows.Close()

	// Checks
	b.WriteString("\n## Checks\n\n")
	rows, err = db.Query(`SELECT round, command, exit_code, timed_out, duration_ms, protected_changes
		FROM checks WHERE request_id=? ORDER BY id`, reqID)
	if err != nil {
		return "", err
	}
	n := 0
	for rows.Next() {
		var round, exit int
		var to bool
		var ms int64
		var cmd, prot string
		rows.Scan(&round, &cmd, &exit, &to, &ms, &prot)
		state := "PASS"
		if to {
			state = "TIMEOUT"
		} else if exit != 0 {
			state = fmt.Sprintf("FAIL (exit %d)", exit)
		}
		fmt.Fprintf(&b, "- round %d: %s `%s` %.1fs", round, state, cmd, float64(ms)/1000)
		if prot != "" {
			fmt.Fprintf(&b, " — protected files reverted: %s", prot)
		}
		b.WriteString("\n")
		n++
	}
	rows.Close()
	if n == 0 {
		b.WriteString("- (no checks configured)\n")
	}

	// Per role
	b.WriteString("\n## By role\n\n| role | runs | turns | tool calls | out tokens | cost | time |\n|---|---|---|---|---|---|---|\n")
	rows, err = db.Query(`SELECT role, COUNT(*), SUM(turns), SUM(tool_calls), SUM(output_tokens), SUM(cost_usd), SUM(duration_ms)
		FROM runs WHERE request_id=? GROUP BY role ORDER BY MIN(id)`, reqID)
	if err != nil {
		return "", err
	}
	var totalCost float64
	var totalMs int64
	for rows.Next() {
		var role string
		var runs, turns, calls int
		var out, ms int64
		var cost float64
		rows.Scan(&role, &runs, &turns, &calls, &out, &cost, &ms)
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | $%.4f | %.0fs |\n", role, runs, turns, calls, out, cost, float64(ms)/1000)
		totalCost += cost
		totalMs += ms
	}
	rows.Close()
	fmt.Fprintf(&b, "| **total** | | | | | **$%.4f** | %.0fs |\n", totalCost, float64(totalMs)/1000)
	b.WriteString("\nCost is the engine's list-price estimate (`*` = run was cut off, summed per response).\n")

	if diffStat != "" {
		fmt.Fprintf(&b, "\n## Changes\n\n```\n%s```\n", diffStat)
	}
	return b.String(), nil
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 80 {
		return string(r[:80]) + "..."
	}
	return strings.ReplaceAll(s, "|", "/")
}
