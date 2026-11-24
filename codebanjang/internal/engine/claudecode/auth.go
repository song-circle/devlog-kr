package claudecode

// Account handling for employees with their own login. Everything goes
// through Claude Code's own `auth` commands: the browser sign-in, the
// callback and the stored credentials belong to Claude Code, never to us.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/song-circle/devlog-kr/codebanjang/internal/proc"
)

// ProfileEnv is the environment for running Claude Code with its own
// configuration folder (a separate login).
func ProfileEnv(dir string) []string {
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(kv), "CLAUDE_CONFIG_DIR=") {
			env = append(env, kv)
		}
	}
	return append(env, "CLAUDE_CONFIG_DIR="+dir)
}

// AuthStatus is the part of `claude auth status` we show.
type AuthStatus struct {
	LoggedIn   bool   `json:"loggedIn"`
	AuthMethod string `json:"authMethod"` // e.g. claude.ai, console, none
	Email      string `json:"email,omitempty"`
	OrgName    string `json:"orgName,omitempty"`
	Plan       string `json:"plan,omitempty"` // subscriptionType, e.g. max
	Error      string `json:"error,omitempty"`
}

// Status asks Claude Code whether the profile (empty = default) is signed in.
func Status(ctx context.Context, profileDir string) AuthStatus {
	exe, err := FindExe()
	if err != nil {
		return AuthStatus{Error: "Claude Code를 찾지 못했습니다: " + err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := proc.NoWindow(exec.CommandContext(ctx, exe, "auth", "status"))
	if profileDir != "" {
		cmd.Env = ProfileEnv(profileDir)
	}
	out, err := cmd.Output()
	var raw map[string]any
	if jerr := json.Unmarshal(out, &raw); jerr != nil {
		msg := strings.TrimSpace(string(out))
		if err != nil {
			msg = err.Error()
		}
		return AuthStatus{Error: msg}
	}
	s := AuthStatus{}
	s.LoggedIn, _ = raw["loggedIn"].(bool)
	s.AuthMethod, _ = raw["authMethod"].(string)
	for _, k := range []string{"email", "emailAddress", "accountEmail"} {
		if v, ok := raw[k].(string); ok && v != "" {
			s.Email = v
		}
	}
	s.Plan, _ = raw["subscriptionType"].(string)
	for _, k := range []string{"orgName", "organizationName", "organization"} {
		if v, ok := raw[k].(string); ok && v != "" {
			s.OrgName = v
		}
	}
	return s
}

// Login runs `claude auth login` for a profile. It opens the browser; the
// sign-in finishes when Claude Code receives its callback (or a pasted code).
type Login struct {
	cmd   *exec.Cmd
	job   *proc.Job
	stdin io.WriteCloser
	mu    sync.Mutex
	lines []string
	done  chan struct{}
	err   error
}

// StartLogin begins a sign-in. console=true bills API usage (Anthropic Console)
// instead of a Claude subscription. onLine receives Claude Code's output.
func StartLogin(profileDir string, console bool, onLine func(string)) (*Login, error) {
	exe, err := FindExe()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		return nil, err
	}
	args := []string{"auth", "login"}
	if console {
		args = append(args, "--console")
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = ProfileEnv(profileDir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	job, err := proc.NewJob(0)
	if err != nil {
		return nil, err
	}
	l := &Login{cmd: cmd, job: job, stdin: stdin, done: make(chan struct{})}
	if err := job.Start(cmd); err != nil {
		job.Close()
		return nil, err
	}
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			line := sc.Text()
			l.mu.Lock()
			l.lines = append(l.lines, line)
			l.mu.Unlock()
			if onLine != nil {
				onLine(line)
			}
		}
	}()
	go func() {
		l.err = cmd.Wait()
		pw.Close()
		job.Close()
		close(l.done)
	}()
	return l, nil
}

// SubmitCode sends a code pasted from the browser page, for when the
// automatic callback does not reach Claude Code.
func (l *Login) SubmitCode(code string) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return fmt.Errorf("빈 코드")
	}
	_, err := io.WriteString(l.stdin, code+"\n")
	return err
}

// Cancel stops a sign-in in progress.
func (l *Login) Cancel() { l.job.Kill() }

// Done is closed when the login process ends.
func (l *Login) Done() <-chan struct{} { return l.done }

// Logout signs a profile out.
func Logout(profileDir string) error {
	exe, err := FindExe()
	if err != nil {
		return err
	}
	cmd := proc.NoWindow(exec.Command(exe, "auth", "logout"))
	cmd.Env = ProfileEnv(profileDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
