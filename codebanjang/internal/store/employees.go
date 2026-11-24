package store

import (
	"database/sql"
	"errors"
	"strings"
)

// Connection kinds for an employee's engine account.
const (
	ConnDefault = "default" // the Claude Code login already on this PC
	ConnOwn     = "own"     // the employee's own login, kept in its own profile folder
	ConnOllama  = "ollama"  // a model served by Ollama on this PC (no account, nothing leaves the PC)
)

// Employee is one AI worker: a role, an engine account and a model.
type Employee struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Role       string `json:"role"` // pm / dev / qa
	Model      string `json:"model"`
	Prompt     string `json:"prompt"`     // extra instructions appended to the role's own
	Connection string `json:"connection"` // default / own
	AuthMode   string `json:"authMode"`   // subscription / console (used when logging in)
	CreatedAt  string `json:"createdAt"`
}

const employeeSchema = `
CREATE TABLE IF NOT EXISTS employees (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	name        TEXT NOT NULL,
	role        TEXT NOT NULL,
	model       TEXT NOT NULL,
	prompt      TEXT NOT NULL DEFAULT '',
	connection  TEXT NOT NULL DEFAULT 'default',
	auth_mode   TEXT NOT NULL DEFAULT 'subscription',
	created_at  TEXT NOT NULL,
	deleted     INTEGER NOT NULL DEFAULT 0
);
`

var validRoles = map[string]bool{"pm": true, "dev": true, "qa": true}

func (e *Employee) validate() error {
	e.Name = strings.TrimSpace(e.Name)
	e.Model = strings.TrimSpace(e.Model)
	if e.Name == "" {
		return errors.New("이름을 적어 주세요")
	}
	if !validRoles[e.Role] {
		return errors.New("역할은 pm, dev, qa 중 하나")
	}
	if e.Model == "" {
		e.Model = "sonnet"
	}
	if e.Connection != ConnOwn && e.Connection != ConnOllama {
		e.Connection = ConnDefault
	}
	if e.AuthMode != "console" {
		e.AuthMode = "subscription"
	}
	return nil
}

// SeedEmployees creates the three default employees on an empty table.
func (s *Store) SeedEmployees() error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM employees`).Scan(&n); err != nil || n > 0 {
		return err
	}
	for _, e := range []Employee{
		{Name: "PM", Role: "pm", Model: "sonnet"},
		{Name: "개발자", Role: "dev", Model: "sonnet"},
		{Name: "QA", Role: "qa", Model: "sonnet"},
	} {
		if _, err := s.SaveEmployee(e); err != nil {
			return err
		}
	}
	return nil
}

// SaveEmployee inserts (ID 0) or updates an employee and returns its id.
func (s *Store) SaveEmployee(e Employee) (int64, error) {
	if err := e.validate(); err != nil {
		return 0, err
	}
	if e.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO employees(name, role, model, prompt, connection, auth_mode, created_at) VALUES (?,?,?,?,?,?,?)`,
			e.Name, e.Role, e.Model, e.Prompt, e.Connection, e.AuthMode, now())
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.db.Exec(`UPDATE employees SET name=?, role=?, model=?, prompt=?, connection=?, auth_mode=? WHERE id=? AND deleted=0`,
		e.Name, e.Role, e.Model, e.Prompt, e.Connection, e.AuthMode, e.ID)
	return e.ID, err
}

// DeleteEmployee hides an employee; past runs keep referring to it.
func (s *Store) DeleteEmployee(id int64) error {
	_, err := s.db.Exec(`UPDATE employees SET deleted=1 WHERE id=?`, id)
	return err
}

func (s *Store) Employees() ([]Employee, error) {
	rows, err := s.db.Query(`SELECT id, name, role, model, prompt, connection, auth_mode, created_at
		FROM employees WHERE deleted=0 ORDER BY CASE role WHEN 'pm' THEN 0 WHEN 'dev' THEN 1 ELSE 2 END, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Employee{}
	for rows.Next() {
		var e Employee
		if err := rows.Scan(&e.ID, &e.Name, &e.Role, &e.Model, &e.Prompt, &e.Connection, &e.AuthMode, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) Employee(id int64) (Employee, error) {
	var e Employee
	err := s.db.QueryRow(`SELECT id, name, role, model, prompt, connection, auth_mode, created_at
		FROM employees WHERE id=? AND deleted=0`, id).Scan(&e.ID, &e.Name, &e.Role, &e.Model, &e.Prompt, &e.Connection, &e.AuthMode, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return e, errors.New("없는 직원입니다")
	}
	return e, err
}
