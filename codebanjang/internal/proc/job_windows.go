// Package proc starts child processes inside a Windows Job Object so that
// cancelling a run also ends every process the child started.
package proc

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Job owns a Windows Job Object. Closing it kills every process in it.
type Job struct {
	h windows.Handle
}

// NewJob creates a job that kills its processes when the handle is closed.
// memLimit > 0 caps the committed memory of each process in bytes.
func NewJob(memLimit uint64) (*Job, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("CreateJobObject: %w", err)
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if memLimit > 0 {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY
		info.ProcessMemoryLimit = uintptr(memLimit)
	}
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("SetInformationJobObject: %w", err)
	}
	return &Job{h: h}, nil
}

// Start starts cmd suspended, puts it in the job, then resumes it, so the
// child cannot spawn anything before it is inside the job.
// cmd also gets its own process group so console Ctrl+C reaches only us.
func (j *Job) Start(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP
	NoWindow(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := uint32(cmd.Process.Pid)
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		cmd.Process.Kill()
		return fmt.Errorf("OpenProcess: %w", err)
	}
	defer windows.CloseHandle(ph)
	if err := windows.AssignProcessToJobObject(j.h, ph); err != nil {
		cmd.Process.Kill()
		return fmt.Errorf("AssignProcessToJobObject: %w", err)
	}
	if err := resumeThreads(pid); err != nil {
		cmd.Process.Kill()
		return err
	}
	return nil
}

// Kill ends every process in the job.
func (j *Job) Kill() error {
	return windows.TerminateJobObject(j.h, 1)
}

// Close kills anything still running and releases the job.
func (j *Job) Close() error {
	if j.h == 0 {
		return nil
	}
	err := windows.CloseHandle(j.h)
	j.h = 0
	return err
}

// resumeThreads resumes every thread of a process started suspended.
func resumeThreads(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("CreateToolhelp32Snapshot: %w", err)
	}
	defer windows.CloseHandle(snap)

	var te windows.ThreadEntry32
	te.Size = uint32(unsafe.Sizeof(te))
	resumed := 0
	for err = windows.Thread32First(snap, &te); err == nil; err = windows.Thread32Next(snap, &te) {
		if te.OwnerProcessID != pid {
			continue
		}
		th, oerr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, te.ThreadID)
		if oerr != nil {
			return fmt.Errorf("OpenThread: %w", oerr)
		}
		_, rerr := windows.ResumeThread(th)
		windows.CloseHandle(th)
		if rerr != nil {
			return fmt.Errorf("ResumeThread: %w", rerr)
		}
		resumed++
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return fmt.Errorf("Thread32Next: %w", err)
	}
	if resumed == 0 {
		return errors.New("no thread found to resume")
	}
	return nil
}
