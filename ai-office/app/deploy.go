package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/song-circle/devlog-kr/ai-office/internal/orchestrator"
	"github.com/song-circle/devlog-kr/ai-office/internal/proc"
	"github.com/song-circle/devlog-kr/ai-office/internal/store"
	"github.com/song-circle/devlog-kr/ai-office/internal/workspace"
)

// The remote that holds the shared branches.
const remote = "origin"

// deployTarget is "origin/dev" for a project with a development branch,
// "" for one that merges into the local folder.
func (a *App) deployTarget(projectID int64) (store.Project, string) {
	p, err := a.st.Project(projectID)
	if err != nil || p.DevBranch == "" {
		return p, ""
	}
	return p, remote + "/" + p.DevBranch
}

// deployDev brings a finished request up to date with the development
// branch, checks it again there and pushes it (see orchestrator.Deploy).
// Progress shows in the request's log like a run.
func (a *App) deployDev(r store.RequestRow, p store.Project) error {
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	if _, busy := a.running[r.ID]; busy {
		a.mu.Unlock()
		cancel()
		return errors.New("이미 진행 중입니다")
	}
	a.running[r.ID] = cancel
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.running, r.ID)
		a.mu.Unlock()
		cancel()
	}()
	start := time.Now()
	o := &orchestrator.Orchestrator{Store: a.st, DataDir: a.dataDir, Log: func(role, line string) {
		runtime.EventsEmit(a.ctx, "log", LogLine{ID: r.ID, Seconds: time.Since(start).Seconds(), Role: role, Line: line})
	}}
	wt := workspace.Open(r.Repo, r.Worktree, r.Branch, r.Base)
	work := wt.Head()
	head, err := o.DeployTo(ctx, orchestrator.Deploy{
		RequestID: r.ID, Worktree: wt, Remote: remote, Target: p.DevBranch,
		Setup: p.Setup, Checks: p.Checks, Timeout: 15 * time.Minute, Deployer: requesterName(p.Repo),
	})
	if err != nil {
		return err
	}
	wt.Remove(false) // keep the branch for history
	return a.st.SetDeployed(r.ID, fmt.Sprintf("%s/%s %s", remote, p.DevBranch, head[:min(12, len(head))]), work)
}

// ReleaseResult is what asking for a production release produced.
type ReleaseResult struct {
	URL     string   `json:"url"`
	Created bool     `json:"created"` // false: a release request was already open
	Changes []string `json:"changes"` // commits on dev not yet on prod
}

// gh runs the GitHub CLI in dir. It uses the person's own GitHub login
// (gh auth login); the app keeps no token.
func gh(dir string, args ...string) (string, error) {
	cmd := proc.NoWindow(exec.Command("gh", args...))
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		var nf *exec.Error
		if errors.As(err, &nf) {
			return "", errors.New("GitHub CLI(gh)가 없습니다. https://cli.github.com 에서 설치하고 명령 프롬프트에서 gh auth login 으로 로그인하세요")
		}
		msg := strings.TrimSpace(errb.String())
		// Checked before the login message: gh suggests "gh auth login"
		// here too, though the person is logged in.
		if strings.Contains(msg, "known GitHub host") {
			return "", errors.New("이 저장소의 origin이 GitHub 저장소가 아닙니다(git remote -v 로 확인). 운영 반영 요청은 GitHub PR로만 만듭니다")
		}
		if strings.Contains(msg, "auth login") || strings.Contains(msg, "not logged") {
			return "", errors.New("GitHub에 로그인되어 있지 않습니다. 명령 프롬프트에서 gh auth login 을 실행하세요")
		}
		return "", fmt.Errorf("gh %s: %s", args[0], msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// releaseBody lists what the release brings, newest first.
func releaseBody(dev, prod string, changes []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s 에 반영된 변경을 %s(운영)에 반영합니다. 승인자가 확인한 뒤 merge합니다.\n\n", dev, prod)
	for _, c := range changes {
		fmt.Fprintf(&b, "- %s\n", c)
	}
	b.WriteString("\n_AI Office에서 만든 반영 요청_\n")
	return b.String()
}

// Release asks for the development branch to go to production: it opens
// a GitHub pull request dev → prod (or returns the one already open).
// Approval and the merge happen on GitHub, by whoever may approve there;
// the app never pushes to the production branch.
func (a *App) Release(projectID int64) (*ReleaseResult, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	p, target := a.deployTarget(projectID)
	if target == "" || !a.inWorkspace(projectID) {
		return nil, errors.New("이 폴더는 개발·운영 브랜치가 설정되어 있지 않습니다(폴더 설정)")
	}
	for _, b := range []string{p.DevBranch, p.ProdBranch} {
		if err := workspace.Fetch(p.Repo, remote, b); err != nil {
			return nil, fmt.Errorf("%s/%s 를 가져오지 못했습니다: %w", remote, b, err)
		}
	}
	changes, err := workspace.Subjects(p.Repo, remote+"/"+p.ProdBranch, remote+"/"+p.DevBranch)
	if err != nil {
		return nil, err
	}
	res := &ReleaseResult{Changes: changes}
	if url, err := gh(p.Repo, "pr", "list", "--base", p.ProdBranch, "--head", p.DevBranch, "--state", "open",
		"--json", "url", "--jq", ".[0].url"); err != nil {
		return nil, err
	} else if url != "" {
		res.URL = url
		runtime.BrowserOpenURL(a.ctx, url)
		return res, nil
	}
	if len(changes) == 0 {
		return nil, fmt.Errorf("%s 에 있는 변경이 이미 모두 %s 에 있습니다", p.DevBranch, p.ProdBranch)
	}
	title := fmt.Sprintf("운영 반영 %s (%d건)", time.Now().Format("2006-01-02"), len(changes))
	url, err := gh(p.Repo, "pr", "create", "--base", p.ProdBranch, "--head", p.DevBranch,
		"--title", title, "--body", releaseBody(p.DevBranch, p.ProdBranch, changes))
	if err != nil {
		return nil, err
	}
	res.URL, res.Created = lastLine(url), true
	runtime.BrowserOpenURL(a.ctx, res.URL)
	return res, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
