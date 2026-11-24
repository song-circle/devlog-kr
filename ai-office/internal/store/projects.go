package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// Project is a registered repository: where the team works, how its work
// is checked, and background every employee should know.
type Project struct {
	ID      int64    `json:"id"`
	Name    string   `json:"name"`
	Repo    string   `json:"repo"`
	Checks  []string `json:"checks"`
	Protect []string `json:"protect"`
	Notes   string   `json:"notes"` // stack, rules, background; added to every prompt
	// Preparing a request's work folder (code-changing modes): files git
	// ignores that the project needs (.env, local config) are copied from
	// the project folder, then Setup commands run (npm ci, code generation).
	CopyFiles []string `json:"copyFiles"`
	Setup     []string `json:"setup"`
	CreatedAt string   `json:"createdAt"`
}

const projectSchema = `
CREATE TABLE IF NOT EXISTS projects (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	name        TEXT NOT NULL,
	repo        TEXT NOT NULL,
	checks_json TEXT NOT NULL DEFAULT '[]',
	protect_json TEXT NOT NULL DEFAULT '[]',
	notes       TEXT NOT NULL DEFAULT '',
	created_at  TEXT NOT NULL,
	deleted     INTEGER NOT NULL DEFAULT 0
);
`

func cleanList(in []string) []string {
	out := []string{}
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// SaveProject inserts (ID 0) or updates a project. The caller resolves Repo
// to a repository root first.
func (s *Store) SaveProject(p Project) (int64, error) {
	p.Name = strings.TrimSpace(p.Name)
	p.Repo = strings.TrimSpace(p.Repo)
	if p.Name == "" {
		return 0, errors.New("프로젝트 이름을 적어 주세요")
	}
	if p.Repo == "" {
		return 0, errors.New("프로젝트 폴더를 골라 주세요")
	}
	checks, _ := json.Marshal(cleanList(p.Checks))
	protect, _ := json.Marshal(cleanList(p.Protect))
	copies, _ := json.Marshal(cleanList(p.CopyFiles))
	setup, _ := json.Marshal(cleanList(p.Setup))
	if p.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO projects(name, repo, checks_json, protect_json, notes, created_at, copy_json, setup_json) VALUES (?,?,?,?,?,?,?,?)`,
			p.Name, p.Repo, string(checks), string(protect), p.Notes, now(), string(copies), string(setup))
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.db.Exec(`UPDATE projects SET name=?, repo=?, checks_json=?, protect_json=?, notes=?, copy_json=?, setup_json=? WHERE id=? AND deleted=0`,
		p.Name, p.Repo, string(checks), string(protect), p.Notes, string(copies), string(setup), p.ID)
	return p.ID, err
}

func (s *Store) DeleteProject(id int64) error {
	_, err := s.db.Exec(`UPDATE projects SET deleted=1 WHERE id=?`, id)
	return err
}

func scanProject(sc interface{ Scan(...any) error }) (Project, error) {
	var p Project
	var checks, protect, copies, setup string
	if err := sc.Scan(&p.ID, &p.Name, &p.Repo, &checks, &protect, &p.Notes, &p.CreatedAt, &copies, &setup); err != nil {
		return p, err
	}
	json.Unmarshal([]byte(checks), &p.Checks)
	json.Unmarshal([]byte(protect), &p.Protect)
	json.Unmarshal([]byte(copies), &p.CopyFiles)
	json.Unmarshal([]byte(setup), &p.Setup)
	if p.CopyFiles == nil {
		p.CopyFiles = []string{}
	}
	if p.Setup == nil {
		p.Setup = []string{}
	}
	if p.Checks == nil {
		p.Checks = []string{}
	}
	if p.Protect == nil {
		p.Protect = []string{}
	}
	return p, nil
}

func (s *Store) Projects() ([]Project, error) {
	rows, err := s.db.Query(`SELECT id, name, repo, checks_json, protect_json, notes, created_at, copy_json, setup_json FROM projects WHERE deleted=0 ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) Project(id int64) (Project, error) {
	p, err := scanProject(s.db.QueryRow(`SELECT id, name, repo, checks_json, protect_json, notes, created_at, copy_json, setup_json FROM projects WHERE id=? AND deleted=0`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, errors.New("없는 프로젝트입니다")
	}
	return p, err
}
