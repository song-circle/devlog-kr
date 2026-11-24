package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLifecycleAndInterrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aio.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRequest(Request{ID: "r1", Text: "do it", Repo: "R", Worktree: "W", Branch: "B", Base: "abc"}); err != nil {
		t.Fatal(err)
	}
	id, err := s.AddTask("r1", 1, "work", "dev", "t", "i")
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{Assigned, InProgress} {
		if err := s.SetTaskStatus(id, st, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddRun(Run{RequestID: "r1", TaskID: id, Role: "dev", Engine: "e", Model: "m", Status: "success",
		CostUSD: 0.25, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	s.AddRun(Run{RequestID: "r1", Role: "pm", Engine: "e", Model: "m", Status: "success", CostUSD: 0.5, StartedAt: time.Now()})
	if c, _ := s.CostSoFar("r1"); c != 0.75 {
		t.Errorf("cost = %v", c)
	}
	s.Close()

	// Another window opening the database while this process still runs
	// the request must leave it alone.
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n, err := s.MarkInterrupted(); err != nil || n != 0 {
		t.Fatalf("MarkInterrupted with a live owner = %d, %v", n, err)
	}

	// Once the owning process is gone, the request was interrupted.
	s.DB().Exec(`UPDATE requests SET owner='4:0' WHERE id='r1'`) // pid 4 (System) with a wrong creation time
	n, err := s.MarkInterrupted()
	if err != nil || n != 1 {
		t.Fatalf("MarkInterrupted = %d, %v", n, err)
	}
	var rs, ts string
	s.DB().QueryRow(`SELECT status FROM requests WHERE id='r1'`).Scan(&rs)
	s.DB().QueryRow(`SELECT status FROM tasks WHERE id=?`, id).Scan(&ts)
	if rs != ReqInterrupted || ts != Failed {
		t.Errorf("request %s task %s", rs, ts)
	}
	var events int
	s.DB().QueryRow(`SELECT COUNT(*) FROM task_events WHERE task_id=?`, id).Scan(&events)
	if events != 4 { // PENDING, ASSIGNED, IN_PROGRESS, FAILED
		t.Errorf("events = %d", events)
	}
}

func TestEmployees(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "aio.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedEmployees(); err != nil {
		t.Fatal(err)
	}
	s.SeedEmployees() // second call must not add more
	es, _ := s.Employees()
	if len(es) != 3 || es[0].Role != "pm" || es[1].Role != "dev" || es[2].Role != "qa" {
		t.Fatalf("seed %+v", es)
	}
	id, err := s.SaveEmployee(Employee{Name: " 김개발 ", Role: "dev", Connection: "own", AuthMode: "console"})
	if err != nil {
		t.Fatal(err)
	}
	e, _ := s.Employee(id)
	if e.Name != "김개발" || e.Model != "sonnet" || e.Connection != ConnOwn || e.AuthMode != "console" {
		t.Errorf("saved %+v", e)
	}
	if _, err := s.SaveEmployee(Employee{Name: "x", Role: "boss"}); err == nil {
		t.Error("bad role accepted")
	}
	s.DeleteEmployee(id)
	if _, err := s.Employee(id); err == nil {
		t.Error("deleted employee still visible")
	}
}

func TestProjects(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "aio.db"))
	defer s.Close()
	id, err := s.SaveProject(Project{Name: " 가계부 ", Repo: "C:/r", Checks: []string{"go test ./...", " "}, Protect: []string{"*_test.go"}, Notes: "Go"})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.Project(id)
	if p.Name != "가계부" || len(p.Checks) != 1 || p.Protect[0] != "*_test.go" || p.Notes != "Go" {
		t.Errorf("%+v", p)
	}
	if _, err := s.SaveProject(Project{Name: "x"}); err == nil {
		t.Error("project without folder accepted")
	}
	s.DeleteProject(id)
	if ps, _ := s.Projects(); len(ps) != 0 {
		t.Errorf("deleted project listed: %+v", ps)
	}
}

func TestWorkspaces(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "aio.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, _ := s.SaveProject(Project{Name: "web", Repo: `C:\work\web`})
	b, _ := s.SaveProject(Project{Name: "api", Repo: `C:\work\api`})
	if p, ok, _ := s.ProjectByRepo(`c:/WORK/web`); !ok || p.ID != a {
		t.Errorf("ProjectByRepo = %v %v", p, ok)
	}
	one, _ := s.SaveWorkspace(Workspace{Name: "web", ProjectIDs: []int64{a}})
	both, _ := s.SaveWorkspace(Workspace{Name: "shop", ProjectIDs: []int64{a, b}})
	if id, _ := s.WorkspaceFor(a); id != one {
		t.Errorf("WorkspaceFor = %d, want %d", id, one)
	}
	s.DeleteProject(b)
	w, err := s.Workspace(both)
	if err != nil || len(w.Projects) != 1 || w.Projects[0].Name != "web" {
		t.Errorf("Workspace = %+v, %v", w, err)
	}
	s.TouchWorkspace(one)
	rs, _ := s.RecentWorkspaces(10)
	if len(rs) != 2 || rs[0].ID != one {
		t.Errorf("recent = %+v", rs)
	}
	s.ForgetWorkspace(one)
	if rs, _ := s.RecentWorkspaces(10); len(rs) != 1 {
		t.Errorf("after forget = %d", len(rs))
	}
	// Requests are matched by folder: one from before projects existed
	// (project 0) still shows, another folder's does not.
	s.CreateRequest(Request{ID: "r-web", Text: "x", Repo: `C:\work\web`, Worktree: "x", Branch: "b", Base: "0", ProjectID: a})
	s.CreateRequest(Request{ID: "r-old", Text: "x", Repo: `c:/Work/web`, Worktree: "x", Branch: "b1", Base: "0"})
	s.CreateRequest(Request{ID: "r-other", Text: "x", Repo: `C:\work\other`, Worktree: "x", Branch: "b2", Base: "0", ProjectID: 99})
	if rs, _ := s.ListRequestsIn([]string{`C:\work\web`}, 10); len(rs) != 2 {
		t.Errorf("ListRequestsIn = %+v", rs)
	}
}
