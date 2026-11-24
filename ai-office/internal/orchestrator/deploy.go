package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/song-circle/devlog-kr/ai-office/internal/check"
	"github.com/song-circle/devlog-kr/ai-office/internal/store"
	"github.com/song-circle/devlog-kr/ai-office/internal/workspace"
)

// Deploy puts a finished request on a shared branch (the development
// branch). Anyone may do it, so the app guarantees what a reviewer would
// otherwise check: the request is first brought up to date with the latest
// branch, the setup and the checks run again on that combined code, and
// only then is it pushed — never forced. If someone pushed in between,
// it starts over with the newer branch.
type Deploy struct {
	RequestID string
	Worktree  *workspace.Worktree
	Remote    string // e.g. "origin"
	Target    string // e.g. "dev"
	Setup     []string
	Checks    []string
	Timeout   time.Duration // per setup / check command
	Attempts  int           // pushes tried when the branch keeps moving (default 3)
	Deployer  string        // who deploys (recorded in the merge commit)
}

// DeployTo runs a Deploy and returns the commit now at the top of the target.
func (o *Orchestrator) DeployTo(ctx context.Context, d Deploy) (string, error) {
	if d.Attempts <= 0 {
		d.Attempts = 3
	}
	if d.Timeout <= 0 {
		d.Timeout = 10 * time.Minute
	}
	wt := d.Worktree
	ref := d.Remote + "/" + d.Target
	dir := filepath.Join(o.DataDir, "requests", d.RequestID)
	os.MkdirAll(dir, 0o755)
	for attempt := 1; attempt <= d.Attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := workspace.Fetch(wt.Repo, d.Remote, d.Target); err != nil {
			return "", fmt.Errorf("%s 를 가져오지 못했습니다: %w", ref, err)
		}
		changed, err := wt.MergeLatest(ref, mergeMessage(ref, d.RequestID, d.Deployer))
		if errors.Is(err, workspace.ErrConflict) {
			return "", fmt.Errorf("다른 사람이 %s 에 먼저 반영한 변경과 충돌합니다(%v). 이 요청은 폐기하고 최신 상태에서 다시 요청하세요", d.Target, err)
		}
		if err != nil {
			return "", err
		}
		if changed {
			o.log("system", fmt.Sprintf("merged latest %s into the request", ref))
		} else {
			o.log("system", fmt.Sprintf("request is already on top of %s", ref))
		}
		// The checks passed on the request alone; run them again whenever
		// newer code came in, and always once before the first push.
		if changed || attempt == 1 {
			if err := o.recheck(ctx, d, dir, attempt, changed); err != nil {
				return "", err
			}
		}
		err = wt.PushTo(d.Remote, d.Target)
		if errors.Is(err, workspace.ErrRejected) {
			o.log("system", fmt.Sprintf("%s moved while checking; trying again (%d/%d)", ref, attempt, d.Attempts))
			continue
		}
		if err != nil {
			return "", fmt.Errorf("push: %w", err)
		}
		head := wt.Head()
		o.log("system", fmt.Sprintf("pushed to %s: %s", ref, head[:min(12, len(head))]))
		return head, nil
	}
	return "", fmt.Errorf("%s 가 계속 바뀌어서 %d번 시도 후 멈췄습니다. 잠시 뒤 다시 반영하세요", d.Target, d.Attempts)
}

// recheck runs the setup (only after newer code came in: dependencies may
// have changed) and the checks in the request's work folder.
func (o *Orchestrator) recheck(ctx context.Context, d Deploy, dir string, attempt int, changed bool) error {
	if changed {
		for i, cmd := range d.Setup {
			res := check.Run(ctx, d.Worktree.Dir, cmd, d.Timeout, filepath.Join(dir, fmt.Sprintf("deploy%d-setup-%d.txt", attempt, i+1)))
			if !res.Passed() {
				return fmt.Errorf("최신 %s 와 합친 뒤 준비 명령 `%s` 실패(exit %d):\n%s", d.Target, cmd, res.ExitCode, check.Tail(res.Output, 2000))
			}
			o.log("system", fmt.Sprintf("setup PASS (%.1fs): %s", res.Duration.Seconds(), cmd))
		}
	}
	for i, cmd := range d.Checks {
		logPath := filepath.Join(dir, fmt.Sprintf("deploy%d-check-%d.txt", attempt, i+1))
		res := check.Run(ctx, d.Worktree.Dir, cmd, d.Timeout, logPath)
		// Rounds from 100 up are the checks before a deploy.
		o.Store.AddCheck(store.Check{RequestID: d.RequestID, Round: 100 + attempt, Command: cmd, ExitCode: res.ExitCode,
			TimedOut: res.TimedOut, Duration: res.Duration, LogPath: logPath})
		if !res.Passed() {
			o.log("qa", fmt.Sprintf("check FAIL before deploy (%.1fs): %s", res.Duration.Seconds(), cmd))
			return fmt.Errorf("최신 %s 와 합친 코드에서 검사 `%s` 실패(exit %d). 다른 사람의 변경과 함께 동작하지 않으니 반영하지 않았습니다:\n%s",
				d.Target, cmd, res.ExitCode, check.Tail(res.Output, 2000))
		}
		o.log("qa", fmt.Sprintf("check PASS before deploy (%.1fs): %s", res.Duration.Seconds(), cmd))
	}
	return nil
}
