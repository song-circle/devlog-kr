package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/song-circle/devlog-kr/ai-office/internal/store"
	"github.com/song-circle/devlog-kr/ai-office/internal/workspace"
)

// A window works on one workspace at a time, like VS Code: one or more
// project folders. Each window is its own process (started with
// --workspace <id>); all windows share the database, the employees and
// their accounts.

// workspaceArg reads --workspace <id> from the command line (0 if absent).
func workspaceArg(args []string) int64 {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--workspace" {
			id, _ := strconv.ParseInt(args[i+1], 10, 64)
			return id
		}
	}
	return 0
}

func (a *App) setWorkspace(id int64) {
	a.mu.Lock()
	a.wsID = id
	a.mu.Unlock()
	title := "AI Office"
	if id != 0 {
		if w, err := a.st.Workspace(id); err == nil {
			title = w.Name + " - AI Office"
			a.st.TouchWorkspace(id)
		}
	}
	if a.ctx != nil {
		runtime.WindowSetTitle(a.ctx, title)
	}
}

func (a *App) currentID() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.wsID
}

// Current is the workspace open in this window (nil = start screen).
func (a *App) Current() (*store.Workspace, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	id := a.currentID()
	if id == 0 {
		return nil, nil
	}
	w, err := a.st.Workspace(id)
	if err != nil {
		a.setWorkspace(0) // forgotten in another window
		return nil, nil
	}
	return &w, nil
}

// RecentWorkspaces lists workspaces for the start screen.
func (a *App) RecentWorkspaces() ([]store.Workspace, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	return a.st.RecentWorkspaces(30)
}

// registerFolder returns the project for a git repository folder,
// registering it (named after the folder) the first time.
func (a *App) registerFolder(dir string) (store.Project, error) {
	top, err := workspace.TopLevel(dir)
	if err != nil {
		return store.Project{}, fmt.Errorf("git 저장소가 아닙니다: %s", dir)
	}
	if p, ok, err := a.st.ProjectByRepo(top); err != nil || ok {
		return p, err
	}
	id, err := a.st.SaveProject(store.Project{Name: filepath.Base(top), Repo: top})
	if err != nil {
		return store.Project{}, err
	}
	return a.st.Project(id)
}

func (a *App) pickDir(title string) (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: title})
}

// OpenFolder asks for a folder and opens it in this window as a workspace
// of its own (reusing the one made last time). Returns false if cancelled.
func (a *App) OpenFolder() (bool, error) {
	if err := a.ready(); err != nil {
		return false, err
	}
	dir, err := a.pickDir("프로젝트 폴더 열기")
	if err != nil || dir == "" {
		return false, err
	}
	p, err := a.registerFolder(dir)
	if err != nil {
		return false, err
	}
	id, err := a.st.WorkspaceFor(p.ID)
	if err != nil {
		return false, err
	}
	if id == 0 {
		if id, err = a.st.SaveWorkspace(store.Workspace{Name: p.Name, ProjectIDs: []int64{p.ID}}); err != nil {
			return false, err
		}
	}
	a.setWorkspace(id)
	return true, nil
}

// OpenWorkspace opens a recent workspace in this window.
func (a *App) OpenWorkspace(id int64) error {
	if err := a.ready(); err != nil {
		return err
	}
	if _, err := a.st.Workspace(id); err != nil {
		return err
	}
	a.setWorkspace(id)
	return nil
}

// CloseWorkspace returns this window to the start screen. Requests still
// running keep running.
func (a *App) CloseWorkspace() {
	a.setWorkspace(0)
}

// NewWindow starts another window (another process of this app), on the
// given workspace or on the start screen (id 0).
func (a *App) NewWindow(id int64) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	var args []string
	if id != 0 {
		args = []string{"--workspace", strconv.FormatInt(id, 10)}
	}
	cmd := exec.Command(exe, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // reap; the window lives on its own
	return nil
}

// ForgetWorkspace removes a workspace from the recent list (folders and
// their settings stay).
func (a *App) ForgetWorkspace(id int64) error {
	if err := a.ready(); err != nil {
		return err
	}
	if id == a.currentID() {
		return errors.New("지금 이 창에 열려 있는 작업공간입니다")
	}
	return a.st.ForgetWorkspace(id)
}

func (a *App) current() (store.Workspace, error) {
	w, err := a.Current()
	if err != nil {
		return store.Workspace{}, err
	}
	if w == nil {
		return store.Workspace{}, errors.New("먼저 폴더나 작업공간을 여세요")
	}
	return *w, nil
}

// AddFolder asks for another folder and adds it to this workspace.
func (a *App) AddFolder() (bool, error) {
	w, err := a.current()
	if err != nil {
		return false, err
	}
	dir, err := a.pickDir("작업공간에 폴더 추가")
	if err != nil || dir == "" {
		return false, err
	}
	p, err := a.registerFolder(dir)
	if err != nil {
		return false, err
	}
	for _, id := range w.ProjectIDs {
		if id == p.ID {
			return false, fmt.Errorf("이미 들어 있는 폴더입니다: %s", p.Name)
		}
	}
	w.ProjectIDs = append(w.ProjectIDs, p.ID)
	_, err = a.st.SaveWorkspace(w)
	return err == nil, err
}

// RemoveFolder takes a folder out of this workspace (its settings and
// request history stay).
func (a *App) RemoveFolder(projectID int64) error {
	w, err := a.current()
	if err != nil {
		return err
	}
	if len(w.ProjectIDs) == 1 {
		return errors.New("작업공간에는 폴더가 하나 이상 있어야 합니다")
	}
	kept := []int64{}
	for _, id := range w.ProjectIDs {
		if id != projectID {
			kept = append(kept, id)
		}
	}
	w.ProjectIDs = kept
	_, err = a.st.SaveWorkspace(w)
	return err
}

// RenameWorkspace renames this window's workspace.
func (a *App) RenameWorkspace(name string) error {
	w, err := a.current()
	if err != nil {
		return err
	}
	w.Name = name
	if _, err := a.st.SaveWorkspace(w); err != nil {
		return err
	}
	a.setWorkspace(w.ID)
	return nil
}

// inWorkspace reports whether a project belongs to this window's workspace.
func (a *App) inWorkspace(projectID int64) bool {
	w, err := a.current()
	if err != nil {
		return false
	}
	for _, id := range w.ProjectIDs {
		if id == projectID {
			return true
		}
	}
	return false
}
