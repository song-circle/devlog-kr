package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/song-circle/devlog-kr/codebanjang/internal/engine/claudecode"
	"github.com/song-circle/devlog-kr/codebanjang/internal/ollama"
	"github.com/song-circle/devlog-kr/codebanjang/internal/orchestrator"
	"github.com/song-circle/devlog-kr/codebanjang/internal/store"
)

// EmployeeView is an employee with the live state of its account.
type EmployeeView struct {
	store.Employee
	Status    claudecode.AuthStatus `json:"status"`
	LoggingIn bool                  `json:"loggingIn"`
}

// Models offered in the model picker. Claude Code also accepts full model
// names, which the picker allows typing.
func (a *App) Models() []string { return []string{"sonnet", "opus", "haiku", "fable"} }

func (a *App) profileDir(id int64) string {
	return filepath.Join(a.dataDir, "profiles", fmt.Sprintf("employee-%d", id))
}

func (a *App) agentFor(e store.Employee) orchestrator.Agent {
	ag := orchestrator.Agent{Name: e.Name, Model: e.Model, Prompt: e.Prompt}
	switch e.Connection {
	case store.ConnOwn:
		ag.ProfileDir = a.profileDir(e.ID)
	case store.ConnOllama:
		// Its own (empty) profile, so no Claude login is used, and every
		// request goes to the local Ollama server.
		ag.ProfileDir = a.profileDir(e.ID)
		ag.Env = ollama.Env(e.Model)
		ag.Local = true
		ag.Native = true // light engine: small models cope with it (bug 014)
	}
	return ag
}

// Employees lists employees with their account status (checked in parallel).
func (a *App) Employees() ([]EmployeeView, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	es, err := a.st.Employees()
	if err != nil {
		return nil, err
	}
	out := make([]EmployeeView, len(es))
	var defaultStatus claudecode.AuthStatus
	var once sync.Once
	var local ollama.Status
	var localOnce sync.Once
	var wg sync.WaitGroup
	for i, e := range es {
		out[i].Employee = e
		a.mu.Lock()
		_, out[i].LoggingIn = a.logins[e.ID]
		a.mu.Unlock()
		wg.Add(1)
		go func(i int, e store.Employee) {
			defer wg.Done()
			if e.Connection == store.ConnOllama {
				localOnce.Do(func() { local = ollama.Check(context.Background()) })
				out[i].Status = ollamaStatus(local, e.Model)
				return
			}
			if e.Connection == store.ConnOwn {
				out[i].Status = claudecode.Status(context.Background(), a.profileDir(e.ID))
				return
			}
			once.Do(func() { defaultStatus = claudecode.Status(context.Background(), "") })
			out[i].Status = defaultStatus
		}(i, e)
	}
	wg.Wait()
	return out, nil
}

func (a *App) SaveEmployee(e store.Employee) (int64, error) {
	if err := a.ready(); err != nil {
		return 0, err
	}
	return a.st.SaveEmployee(e)
}

// DeleteEmployee hides the employee. Its own login stays in its profile
// folder until it is logged out.
func (a *App) DeleteEmployee(id int64) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.st.DeleteEmployee(id)
}

// Ollama reports the local server and its installed models.
func (a *App) Ollama() ollama.Status { return ollama.Check(context.Background()) }

// OllamaProbes returns saved model tests (model -> result).
func (a *App) OllamaProbes() map[string]ollama.ProbeResult {
	out := map[string]ollama.ProbeResult{}
	if a.ready() == nil {
		if v, _ := a.st.Setting("ollama_probe"); v != "" {
			json.Unmarshal([]byte(v), &out)
		}
	}
	return out
}

// ProbeModel tests whether a local model can work as an employee (reads a
// file through a tool call and answers in JSON) and saves the result.
func (a *App) ProbeModel(model string) (ollama.ProbeResult, error) {
	if err := a.ready(); err != nil {
		return ollama.ProbeResult{}, err
	}
	profile := filepath.Join(a.dataDir, "profiles", "probe-"+strings.NewReplacer(":", "_", "/", "_").Replace(model))
	r := ollama.Probe(context.Background(), model, profile, 6*time.Minute)
	all := a.OllamaProbes()
	all[model] = r
	b, _ := json.Marshal(all)
	a.st.SetSetting("ollama_probe", string(b))
	return r, nil
}

// ollamaStatus shows a local-model employee in the same shape as an account.
func ollamaStatus(s ollama.Status, model string) claudecode.AuthStatus {
	switch {
	case !s.Running:
		return claudecode.AuthStatus{AuthMethod: "ollama", Error: s.Error}
	case !s.Has(model):
		return claudecode.AuthStatus{AuthMethod: "ollama", Error: "모델이 설치되어 있지 않습니다: " + model + " (ollama pull " + model + ")"}
	}
	return claudecode.AuthStatus{LoggedIn: true, AuthMethod: "ollama", Plan: "Ollama " + s.Version}
}

// Login starts the official Claude Code sign-in for an employee with its own
// account. The browser opens; "login" events carry Claude Code's output (the
// fallback link) and "login-done" fires when it ends.
func (a *App) Login(id int64) error {
	if err := a.ready(); err != nil {
		return err
	}
	e, err := a.st.Employee(id)
	if err != nil {
		return err
	}
	if e.Connection != store.ConnOwn {
		return errors.New("이 직원은 이 PC의 기본 로그인을 씁니다. 따로 로그인하려면 연결 방식을 '전용 계정'으로 바꾸세요")
	}
	a.mu.Lock()
	if _, busy := a.logins[id]; busy {
		a.mu.Unlock()
		return errors.New("이미 로그인 중입니다")
	}
	a.mu.Unlock()
	l, err := claudecode.StartLogin(a.profileDir(id), e.AuthMode == "console", func(line string) {
		runtime.EventsEmit(a.ctx, "login", map[string]any{"id": id, "line": line})
	})
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.logins[id] = l
	a.mu.Unlock()
	go func() {
		<-l.Done()
		a.mu.Lock()
		delete(a.logins, id)
		a.mu.Unlock()
		st := claudecode.Status(context.Background(), a.profileDir(id))
		runtime.EventsEmit(a.ctx, "login-done", map[string]any{"id": id, "status": st})
	}()
	return nil
}

// LoginCode passes a code shown on the browser page to a sign-in in progress.
func (a *App) LoginCode(id int64, code string) error {
	a.mu.Lock()
	l := a.logins[id]
	a.mu.Unlock()
	if l == nil {
		return errors.New("진행 중인 로그인이 없습니다")
	}
	return l.SubmitCode(code)
}

func (a *App) CancelLogin(id int64) {
	a.mu.Lock()
	l := a.logins[id]
	a.mu.Unlock()
	if l != nil {
		l.Cancel()
	}
}

func (a *App) Logout(id int64) error {
	if err := a.ready(); err != nil {
		return err
	}
	e, err := a.st.Employee(id)
	if err != nil {
		return err
	}
	if e.Connection != store.ConnOwn {
		return errors.New("기본 로그인은 앱에서 로그아웃하지 않습니다(이 PC의 Claude Code 로그인)")
	}
	return claudecode.Logout(a.profileDir(id))
}
