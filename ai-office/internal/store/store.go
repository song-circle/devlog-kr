// Package store keeps every request, task, state change, engine run and
// check in a local SQLite database, so nothing is lost when the app exits
// and every measurement can be recomputed later.
package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/song-circle/devlog-kr/ai-office/internal/proc"
)

// Task states (원본 기획 4.2).
const (
	Pending    = "PENDING"
	Assigned   = "ASSIGNED"
	InProgress = "IN_PROGRESS"
	Review     = "REVIEW"
	Revision   = "REVISION"
	Completed  = "COMPLETED"
	Failed     = "FAILED"
	Cancelled  = "CANCELLED"
)

// Request states.
const (
	ReqRunning     = "RUNNING"
	ReqDone        = "DONE"        // all tasks done and checks passed; waiting for approval
	ReqFailed      = "FAILED"      // gave up (limits, engine failure, checks never passed)
	ReqCancelled   = "CANCELLED"   // stopped by the user
	ReqInterrupted = "INTERRUPTED" // the app ended while it was running
)

const schema = `
CREATE TABLE IF NOT EXISTS requests (
	id            TEXT PRIMARY KEY,
	text          TEXT NOT NULL,
	repo          TEXT NOT NULL,
	worktree      TEXT NOT NULL,
	branch        TEXT NOT NULL,
	base          TEXT NOT NULL,
	status        TEXT NOT NULL,
	reason        TEXT NOT NULL DEFAULT '',
	plan_summary  TEXT NOT NULL DEFAULT '',
	config_json   TEXT NOT NULL DEFAULT '{}',
	created_at    TEXT NOT NULL,
	finished_at   TEXT
);
CREATE TABLE IF NOT EXISTS tasks (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	request_id    TEXT NOT NULL REFERENCES requests(id),
	seq           INTEGER NOT NULL,
	kind          TEXT NOT NULL,          -- plan / work / fix
	role          TEXT NOT NULL,          -- pm / dev
	title         TEXT NOT NULL,
	instructions  TEXT NOT NULL,
	status        TEXT NOT NULL,
	attempts      INTEGER NOT NULL DEFAULT 0,
	result        TEXT NOT NULL DEFAULT '',
	created_at    TEXT NOT NULL,
	updated_at    TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS task_events (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	task_id       INTEGER NOT NULL REFERENCES tasks(id),
	from_status   TEXT NOT NULL,
	to_status     TEXT NOT NULL,
	note          TEXT NOT NULL DEFAULT '',
	at            TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runs (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	request_id    TEXT NOT NULL REFERENCES requests(id),
	task_id       INTEGER REFERENCES tasks(id),
	role          TEXT NOT NULL,
	engine        TEXT NOT NULL,
	model         TEXT NOT NULL,
	status        TEXT NOT NULL,
	turns         INTEGER NOT NULL,
	tool_calls    INTEGER NOT NULL,
	tool_errors   INTEGER NOT NULL,
	denials       INTEGER NOT NULL,
	input_tokens  INTEGER NOT NULL,
	output_tokens INTEGER NOT NULL,
	cache_read    INTEGER NOT NULL,
	cache_write   INTEGER NOT NULL,
	cost_usd      REAL NOT NULL,
	cost_estimated INTEGER NOT NULL,
	duration_ms   INTEGER NOT NULL,
	log_dir       TEXT NOT NULL,
	result_text   TEXT NOT NULL DEFAULT '',
	started_at    TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS checks (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	request_id    TEXT NOT NULL REFERENCES requests(id),
	task_id       INTEGER REFERENCES tasks(id),
	round         INTEGER NOT NULL,
	command       TEXT NOT NULL,
	exit_code     INTEGER NOT NULL,
	timed_out     INTEGER NOT NULL,
	duration_ms   INTEGER NOT NULL,
	protected_changes TEXT NOT NULL DEFAULT '', -- protected files the agent changed (reverted)
	log_path      TEXT NOT NULL,
	at            TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (
	key           TEXT PRIMARY KEY,
	value         TEXT NOT NULL,
	updated_at    TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS bench_runs (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	bench_id      TEXT NOT NULL,
	task          TEXT NOT NULL,
	request_id    TEXT NOT NULL REFERENCES requests(id),
	mode          TEXT NOT NULL,          -- team / solo
	model         TEXT NOT NULL,
	rep           INTEGER NOT NULL,
	graded        INTEGER NOT NULL,       -- 1 if the hidden grading command ran
	grade_passed  INTEGER NOT NULL,       -- hidden grading command exit 0
	tests_passed  INTEGER NOT NULL,
	tests_failed  INTEGER NOT NULL,
	grade_log     TEXT NOT NULL,
	at            TEXT NOT NULL
);
`

// BenchRun is one graded repetition of a benchmark.
type BenchRun struct {
	BenchID, Task, RequestID, Mode, Model string
	Rep                                   int
	Graded, GradePassed                   bool
	TestsPassed, TestsFailed              int
	GradeLog                              string
}

func (s *Store) AddBenchRun(b BenchRun) error {
	_, err := s.db.Exec(`INSERT INTO bench_runs(bench_id, task, request_id, mode, model, rep, graded, grade_passed, tests_passed, tests_failed, grade_log, at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, b.BenchID, b.Task, b.RequestID, b.Mode, b.Model, b.Rep, b.Graded, b.GradePassed,
		b.TestsPassed, b.TestsFailed, b.GradeLog, now())
	return err
}

// Store wraps the database.
type Store struct{ db *sql.DB }

func now() string { return time.Now().Format(time.RFC3339Nano) }

// Open opens (and creates) the database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema + employeeSchema + projectSchema + messageSchema + workspaceSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	// Columns added after the first release; "duplicate column" means done.
	for _, m := range []string{
		`ALTER TABLE runs ADD COLUMN agent TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE requests ADD COLUMN mode TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE requests ADD COLUMN project_id INTEGER NOT NULL DEFAULT 0`,
		// The process running the request (proc.SelfToken), so another
		// window or a command-line run does not mark it interrupted.
		`ALTER TABLE requests ADD COLUMN owner TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE projects ADD COLUMN copy_json TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE projects ADD COLUMN dev_branch TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE projects ADD COLUMN prod_branch TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE requests ADD COLUMN work_commit TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE projects ADD COLUMN setup_json TEXT NOT NULL DEFAULT '[]'`,
	} {
		if _, err := db.Exec(m); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("migrate: %w", err)
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// DB exposes the handle for reports and tests.
func (s *Store) DB() *sql.DB { return s.db }

// MarkInterrupted flags requests and tasks left running by a process that
// has ended. Requests of processes still running (another window, a
// command-line run) are left alone. Returns how many requests were affected.
func (s *Store) MarkInterrupted() (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var dead []string
	reqs, err := tx.Query(`SELECT id, owner FROM requests WHERE status = ?`, ReqRunning)
	if err != nil {
		return 0, err
	}
	for reqs.Next() {
		var id, owner string
		reqs.Scan(&id, &owner)
		if !proc.Alive(owner) {
			dead = append(dead, id)
		}
	}
	reqs.Close()
	if len(dead) == 0 {
		return 0, nil
	}
	in := "?" + strings.Repeat(",?", len(dead)-1)
	args := []any{ReqRunning, Assigned, InProgress, Review, Revision}
	for _, id := range dead {
		args = append(args, id)
	}
	rows, err := tx.Query(`SELECT t.id, t.status FROM tasks t JOIN requests r ON r.id = t.request_id
		WHERE r.status = ? AND t.status IN (?, ?, ?, ?) AND r.id IN (`+in+`)`, args...)
	if err != nil {
		return 0, err
	}
	type ts struct {
		id     int64
		status string
	}
	var open []ts
	for rows.Next() {
		var t ts
		rows.Scan(&t.id, &t.status)
		open = append(open, t)
	}
	rows.Close()
	for _, t := range open {
		if _, err := tx.Exec(`UPDATE tasks SET status=?, updated_at=? WHERE id=?`, Failed, now(), t.id); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`INSERT INTO task_events(task_id, from_status, to_status, note, at) VALUES (?,?,?,?,?)`,
			t.id, t.status, Failed, "interrupted: app ended while running", now()); err != nil {
			return 0, err
		}
	}
	args = []any{ReqInterrupted, "app ended while running", now(), ReqRunning}
	for _, id := range dead {
		args = append(args, id)
	}
	res, err := tx.Exec(`UPDATE requests SET status=?, reason=?, finished_at=? WHERE status=? AND id IN (`+in+`)`, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, tx.Commit()
}

// Request is a new request row.
type Request struct {
	ID, Text, Repo, Worktree, Branch, Base, ConfigJSON string
	Mode                                               string
	ProjectID                                          int64
}

func (s *Store) CreateRequest(r Request) error {
	_, err := s.db.Exec(`INSERT INTO requests(id, text, repo, worktree, branch, base, status, config_json, created_at, mode, project_id, owner)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, r.ID, r.Text, r.Repo, r.Worktree, r.Branch, r.Base, ReqRunning, r.ConfigJSON, now(), r.Mode, r.ProjectID,
		proc.SelfToken())
	return err
}

func (s *Store) SetPlanSummary(reqID, summary string) error {
	_, err := s.db.Exec(`UPDATE requests SET plan_summary=? WHERE id=?`, summary, reqID)
	return err
}

func (s *Store) FinishRequest(reqID, status, reason string) error {
	_, err := s.db.Exec(`UPDATE requests SET status=?, reason=?, finished_at=? WHERE id=?`, status, reason, now(), reqID)
	return err
}

// AddTask creates a task in PENDING and returns its id.
func (s *Store) AddTask(reqID string, seq int, kind, role, title, instructions string) (int64, error) {
	t := now()
	res, err := s.db.Exec(`INSERT INTO tasks(request_id, seq, kind, role, title, instructions, status, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)`, reqID, seq, kind, role, title, instructions, Pending, t, t)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	_, err = s.db.Exec(`INSERT INTO task_events(task_id, from_status, to_status, note, at) VALUES (?,?,?,?,?)`,
		id, "", Pending, "", t)
	return id, err
}

// SetTaskStatus changes a task's status and records the change.
func (s *Store) SetTaskStatus(taskID int64, to, note string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var from string
	if err := tx.QueryRow(`SELECT status FROM tasks WHERE id=?`, taskID).Scan(&from); err != nil {
		return err
	}
	t := now()
	if _, err := tx.Exec(`UPDATE tasks SET status=?, updated_at=? WHERE id=?`, to, t, taskID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO task_events(task_id, from_status, to_status, note, at) VALUES (?,?,?,?,?)`,
		taskID, from, to, note, t); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetTaskResult(taskID int64, attempts int, result string) error {
	_, err := s.db.Exec(`UPDATE tasks SET attempts=?, result=?, updated_at=? WHERE id=?`, attempts, result, now(), taskID)
	return err
}

// Run is one engine run row.
type Run struct {
	RequestID                             string
	TaskID                                int64
	Role, Engine, Model, Status           string
	Agent                                 string // employee name
	Turns, ToolCalls, ToolErrors, Denials int
	InputTokens, OutputTokens, CacheRead  int64
	CacheWrite                            int64
	CostUSD                               float64
	CostEstimated                         bool
	Duration                              time.Duration
	LogDir, ResultText                    string
	StartedAt                             time.Time
}

func (s *Store) AddRun(r Run) error {
	_, err := s.db.Exec(`INSERT INTO runs(request_id, task_id, role, engine, model, status, turns, tool_calls, tool_errors, denials,
		input_tokens, output_tokens, cache_read, cache_write, cost_usd, cost_estimated, duration_ms, log_dir, result_text, started_at, agent)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.RequestID, nullID(r.TaskID), r.Role, r.Engine, r.Model, r.Status, r.Turns, r.ToolCalls, r.ToolErrors, r.Denials,
		r.InputTokens, r.OutputTokens, r.CacheRead, r.CacheWrite, r.CostUSD, r.CostEstimated, r.Duration.Milliseconds(),
		r.LogDir, r.ResultText, r.StartedAt.Format(time.RFC3339Nano), r.Agent)
	return err
}

// Check is one check row.
type Check struct {
	RequestID        string
	TaskID           int64
	Round            int
	Command          string
	ExitCode         int
	TimedOut         bool
	Duration         time.Duration
	ProtectedChanges string
	LogPath          string
}

func (s *Store) AddCheck(c Check) error {
	_, err := s.db.Exec(`INSERT INTO checks(request_id, task_id, round, command, exit_code, timed_out, duration_ms, protected_changes, log_path, at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, c.RequestID, nullID(c.TaskID), c.Round, c.Command, c.ExitCode, c.TimedOut,
		c.Duration.Milliseconds(), c.ProtectedChanges, c.LogPath, now())
	return err
}

// CostSoFar sums the cost of all runs of a request.
func (s *Store) CostSoFar(reqID string) (float64, error) {
	var c sql.NullFloat64
	err := s.db.QueryRow(`SELECT SUM(cost_usd) FROM runs WHERE request_id=?`, reqID).Scan(&c)
	return c.Float64, err
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
