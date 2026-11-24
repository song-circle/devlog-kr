package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
)

// Workspace is what one window works on, like a VS Code workspace: one or
// more registered project folders opened together.
type Workspace struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	ProjectIDs []int64   `json:"projectIds"`
	Projects   []Project `json:"projects"` // filled by Workspace / RecentWorkspaces
	OpenedAt   string    `json:"openedAt"`
}

const workspaceSchema = `
CREATE TABLE IF NOT EXISTS workspaces (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	name          TEXT NOT NULL,
	projects_json TEXT NOT NULL DEFAULT '[]',
	opened_at     TEXT NOT NULL,
	deleted       INTEGER NOT NULL DEFAULT 0
);
`

// SamePath compares Windows paths (case and separators do not matter).
func SamePath(a, b string) bool {
	return pathKey(a) == pathKey(b)
}

// pathKey is a path in the form ListRequestsIn compares (SQLite lower()
// folds ASCII only, so this does too).
func pathKey(p string) string {
	p = strings.ReplaceAll(filepath.Clean(p), "/", `\`)
	b := []byte(p)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// ProjectByRepo finds the registered project for a repository root.
func (s *Store) ProjectByRepo(repo string) (Project, bool, error) {
	ps, err := s.Projects()
	if err != nil {
		return Project{}, false, err
	}
	for _, p := range ps {
		if SamePath(p.Repo, repo) {
			return p, true, nil
		}
	}
	return Project{}, false, nil
}

// SaveWorkspace inserts (ID 0) or updates a workspace and marks it opened now.
func (s *Store) SaveWorkspace(w Workspace) (int64, error) {
	w.Name = strings.TrimSpace(w.Name)
	if w.Name == "" {
		return 0, errors.New("작업공간 이름을 적어 주세요")
	}
	if w.ProjectIDs == nil {
		w.ProjectIDs = []int64{}
	}
	ids, _ := json.Marshal(w.ProjectIDs)
	if w.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO workspaces(name, projects_json, opened_at) VALUES (?,?,?)`, w.Name, string(ids), now())
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.db.Exec(`UPDATE workspaces SET name=?, projects_json=?, opened_at=? WHERE id=? AND deleted=0`, w.Name, string(ids), now(), w.ID)
	return w.ID, err
}

// TouchWorkspace moves a workspace to the top of the recent list.
func (s *Store) TouchWorkspace(id int64) error {
	_, err := s.db.Exec(`UPDATE workspaces SET opened_at=? WHERE id=?`, now(), id)
	return err
}

// ForgetWorkspace removes a workspace from the recent list (its folders stay registered).
func (s *Store) ForgetWorkspace(id int64) error {
	_, err := s.db.Exec(`UPDATE workspaces SET deleted=1 WHERE id=?`, id)
	return err
}

// fillWorkspace decodes the project ids and loads the projects that still exist.
func (s *Store) fillWorkspace(w *Workspace, ids string) {
	var all []int64
	json.Unmarshal([]byte(ids), &all)
	w.ProjectIDs = []int64{}
	w.Projects = []Project{}
	for _, id := range all {
		p, err := s.Project(id)
		if err != nil {
			continue // project deleted since
		}
		w.Projects = append(w.Projects, p)
		w.ProjectIDs = append(w.ProjectIDs, id)
	}
}

// Workspace returns one workspace with its projects.
func (s *Store) Workspace(id int64) (Workspace, error) {
	var w Workspace
	var ids string
	err := s.db.QueryRow(`SELECT id, name, projects_json, opened_at FROM workspaces WHERE id=? AND deleted=0`, id).
		Scan(&w.ID, &w.Name, &ids, &w.OpenedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return w, errors.New("없는 작업공간입니다")
	}
	if err != nil {
		return w, err
	}
	s.fillWorkspace(&w, ids)
	return w, nil
}

// RecentWorkspaces lists workspaces, most recently opened first.
func (s *Store) RecentWorkspaces(limit int) ([]Workspace, error) {
	rows, err := s.db.Query(`SELECT id, name, projects_json, opened_at FROM workspaces WHERE deleted=0 ORDER BY opened_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	type row struct {
		w   Workspace
		ids string
	}
	var rs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.w.ID, &r.w.Name, &r.ids, &r.w.OpenedAt); err != nil {
			rows.Close()
			return nil, err
		}
		rs = append(rs, r)
	}
	rows.Close() // single connection: close before loading projects
	out := []Workspace{}
	for _, r := range rs {
		s.fillWorkspace(&r.w, r.ids)
		out = append(out, r.w)
	}
	return out, nil
}

// WorkspaceFor finds the workspace holding exactly this one project (what
// "open folder" reopens), or 0.
func (s *Store) WorkspaceFor(projectID int64) (int64, error) {
	ws, err := s.RecentWorkspaces(1000)
	if err != nil {
		return 0, err
	}
	for _, w := range ws {
		if len(w.ProjectIDs) == 1 && w.ProjectIDs[0] == projectID {
			return w.ID, nil
		}
	}
	return 0, nil
}
