package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Statuses set by the user after a request is DONE.
const (
	ReqApproved  = "APPROVED"  // merged into the user's branch
	ReqDiscarded = "DISCARDED" // worktree and branch removed
)

// RequestRow is a request with its totals, for lists.
type RequestRow struct {
	ID        string  `json:"id"`
	Text      string  `json:"text"`
	Repo      string  `json:"repo"`
	Worktree  string  `json:"worktree"`
	Branch    string  `json:"branch"`
	Base      string  `json:"base"`
	Status    string  `json:"status"`
	Reason    string  `json:"reason"`
	Plan      string  `json:"plan"`
	Config    string  `json:"config"`
	CreatedAt string  `json:"createdAt"`
	Finished  string  `json:"finishedAt"`
	CostUSD   float64 `json:"costUsd"`
	Runs      int     `json:"runs"`
	Mode      string  `json:"mode"`
	ProjectID int64   `json:"projectId"`
	Project   string  `json:"project"` // project name ("" if none)
}

const requestCols = `r.id, r.text, r.repo, r.worktree, r.branch, r.base, r.status, r.reason, r.plan_summary, r.config_json,
	r.created_at, COALESCE(r.finished_at,''), COALESCE(SUM(u.cost_usd),0), COUNT(u.id),
	r.mode, r.project_id, COALESCE((SELECT name FROM projects p WHERE p.id = r.project_id),'')`

func scanRequest(sc interface{ Scan(...any) error }) (RequestRow, error) {
	var r RequestRow
	err := sc.Scan(&r.ID, &r.Text, &r.Repo, &r.Worktree, &r.Branch, &r.Base, &r.Status, &r.Reason, &r.Plan, &r.Config,
		&r.CreatedAt, &r.Finished, &r.CostUSD, &r.Runs, &r.Mode, &r.ProjectID, &r.Project)
	return r, err
}

// ListRequests returns the newest requests first.
func (s *Store) ListRequests(limit int) ([]RequestRow, error) {
	rows, err := s.db.Query(`SELECT `+requestCols+` FROM requests r LEFT JOIN runs u ON u.request_id = r.id
		GROUP BY r.id ORDER BY r.created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RequestRow{} // never null in JSON
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListRequestsIn returns the newest requests made on the given repository
// folders. Matching by folder rather than project id keeps requests made
// before projects existed, or under a project registered again.
func (s *Store) ListRequestsIn(repos []string, limit int) ([]RequestRow, error) {
	out := []RequestRow{}
	if len(repos) == 0 {
		return out, nil
	}
	args := []any{}
	for _, r := range repos {
		args = append(args, pathKey(r))
	}
	args = append(args, limit)
	rows, err := s.db.Query(`SELECT `+requestCols+` FROM requests r LEFT JOIN runs u ON u.request_id = r.id
		WHERE lower(replace(r.repo, '/', '\')) IN (?`+strings.Repeat(",?", len(repos)-1)+`)
		GROUP BY r.id ORDER BY r.created_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRequest returns one request.
func (s *Store) GetRequest(id string) (RequestRow, error) {
	return scanRequest(s.db.QueryRow(`SELECT `+requestCols+` FROM requests r LEFT JOIN runs u ON u.request_id = r.id
		WHERE r.id = ? GROUP BY r.id`, id))
}

// SetRequestStatus records the user's decision on a finished request.
func (s *Store) SetRequestStatus(id, status, reason string) error {
	_, err := s.db.Exec(`UPDATE requests SET status=?, reason=? WHERE id=?`, status, reason, id)
	return err
}

// TaskRow is a task with its run totals.
type TaskRow struct {
	ID       int64   `json:"id"`
	Seq      int     `json:"seq"`
	Kind     string  `json:"kind"`
	Role     string  `json:"role"`
	Title    string  `json:"title"`
	Status   string  `json:"status"`
	Result   string  `json:"result"`
	CostUSD  float64 `json:"costUsd"`
	Seconds  float64 `json:"seconds"`
	Estimate bool    `json:"estimated"`
}

func (s *Store) Tasks(reqID string) ([]TaskRow, error) {
	rows, err := s.db.Query(`SELECT t.id, t.seq, t.kind, t.role, t.title, t.status, t.result,
			COALESCE(SUM(r.cost_usd),0), COALESCE(SUM(r.duration_ms),0)/1000.0, COALESCE(MAX(r.cost_estimated),0)
		FROM tasks t LEFT JOIN runs r ON r.task_id = t.id WHERE t.request_id=? GROUP BY t.id ORDER BY t.id`, reqID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TaskRow{}
	for rows.Next() {
		var t TaskRow
		if err := rows.Scan(&t.ID, &t.Seq, &t.Kind, &t.Role, &t.Title, &t.Status, &t.Result, &t.CostUSD, &t.Seconds, &t.Estimate); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CheckRow is one check result.
type CheckRow struct {
	Round     int     `json:"round"`
	Command   string  `json:"command"`
	ExitCode  int     `json:"exitCode"`
	TimedOut  bool    `json:"timedOut"`
	Seconds   float64 `json:"seconds"`
	Protected string  `json:"protected"`
	LogPath   string  `json:"logPath"`
}

func (s *Store) Checks(reqID string) ([]CheckRow, error) {
	rows, err := s.db.Query(`SELECT round, command, exit_code, timed_out, duration_ms/1000.0, protected_changes, log_path
		FROM checks WHERE request_id=? ORDER BY id`, reqID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CheckRow{}
	for rows.Next() {
		var c CheckRow
		if err := rows.Scan(&c.Round, &c.Command, &c.ExitCode, &c.TimedOut, &c.Seconds, &c.Protected, &c.LogPath); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Setting reads a value saved with SetSetting ("" if missing).
func (s *Store) Setting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings(key, value, updated_at) VALUES (?,?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`, key, value, time.Now().Format(time.RFC3339))
	return err
}
