// Package check runs user-defined verification commands (build, test) in a
// worktree. These commands come from the user, never from the agent.
package check

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/song-circle/devlog-kr/codebanjang/internal/proc"
)

// Result of one check run.
type Result struct {
	Command  string        `json:"command"`
	ExitCode int           `json:"exit_code"` // -1 if it could not start
	TimedOut bool          `json:"timed_out"`
	Duration time.Duration `json:"duration"`
	Output   string        `json:"-"` // combined stdout+stderr
}

// Passed reports whether the check succeeded.
func (r *Result) Passed() bool { return r.ExitCode == 0 && !r.TimedOut }

// Run executes command through cmd.exe in dir, inside a job so a timeout
// kills everything it started. Output is also written to logPath if set.
func Run(ctx context.Context, dir, command string, timeout time.Duration, logPath string) *Result {
	res := &Result{Command: command, ExitCode: -1}
	var buf bytes.Buffer
	var out io.Writer = &buf
	if logPath != "" {
		if f, err := os.Create(logPath); err == nil {
			defer f.Close()
			out = io.MultiWriter(&buf, f)
		}
	}
	defer func() { res.Output = buf.String() }()

	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /S /C "` + command + `"`}
	cmd.Dir = dir
	cmd.Stdout = out
	cmd.Stderr = out
	job, err := proc.NewJob(0)
	if err != nil {
		fmt.Fprintf(out, "job: %v\n", err)
		return res
	}
	defer job.Close()
	start := time.Now()
	if err := job.Start(cmd); err != nil {
		fmt.Fprintf(out, "start: %v\n", err)
		return res
	}
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan struct{})
	go func() {
		select {
		case <-cctx.Done():
			job.Kill()
		case <-done:
		}
	}()
	cmd.Wait()
	close(done)
	res.Duration = time.Since(start)
	res.TimedOut = cctx.Err() == context.DeadlineExceeded
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	return res
}

// Tail returns the last n bytes of s, cut at a line start.
func Tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	if i := bytes.IndexByte([]byte(s), '\n'); i >= 0 && i < len(s)-1 {
		s = s[i+1:]
	}
	return "...\n" + s
}
