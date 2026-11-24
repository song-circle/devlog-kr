// Package workspace isolates each request in its own git worktree and branch
// so the agent never touches the user's working folder or target branch.
package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/song-circle/devlog-kr/codebanjang/internal/proc"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Worktree is one isolated checkout.
type Worktree struct {
	Repo   string // original repository (top level)
	Dir    string // worktree directory
	Branch string
	Base   string // commit the branch started from
}

// Change is one changed path as reported by git status.
type Change struct {
	Status string // two-letter porcelain code, e.g. " M", "??"
	Path   string
}

func git(dir string, args ...string) (string, error) {
	cmd := proc.NoWindow(exec.Command("git", append([]string{"-C", dir}, args...)...))
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// TopLevel returns the repository root containing dir.
func TopLevel(dir string) (string, error) {
	out, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return filepath.Clean(strings.TrimSpace(out)), nil
}

// Create adds a worktree at root/id on a new branch aio/<id> from HEAD.
func Create(repo, root, id string) (*Worktree, error) {
	top, err := TopLevel(repo)
	if err != nil {
		return nil, err
	}
	base, err := git(top, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("repository has no commit yet: %w", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	w := &Worktree{
		Repo:   top,
		Dir:    filepath.Join(root, id),
		Branch: "cb/" + id,
		Base:   strings.TrimSpace(base),
	}
	if _, err := git(top, "worktree", "add", "-b", w.Branch, w.Dir, w.Base); err != nil {
		return nil, err
	}
	return w, nil
}

// ReadOnly describes the project folder itself for modes that only read:
// no worktree and no branch, Dir is the repository root.
func ReadOnly(repo string) (*Worktree, error) {
	top, err := TopLevel(repo)
	if err != nil {
		return nil, err
	}
	base, _ := git(top, "rev-parse", "HEAD")
	return &Worktree{Repo: top, Dir: top, Base: strings.TrimSpace(base)}, nil
}

// Status lists changed and untracked files in dir.
func Status(dir string) ([]Change, error) {
	out, err := git(dir, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var changes []Change
	parts := strings.Split(out, "\x00")
	for i := 0; i < len(parts); i++ {
		p := parts[i]
		if len(p) < 4 {
			continue
		}
		c := Change{Status: p[:2], Path: p[3:]}
		if c.Status[0] == 'R' || c.Status[0] == 'C' {
			i++ // the original path follows a rename/copy entry
		}
		changes = append(changes, c)
	}
	return changes, nil
}

// PathStatus is the two-letter git status of one path in repo ("" when it
// is committed and unchanged, "??" when git does not track it yet).
func PathStatus(repo, path string) (string, error) {
	out, err := git(repo, "status", "--porcelain=v1", "--untracked-files=all", "--", path)
	if err != nil {
		return "", err
	}
	if len(out) < 2 {
		return "", nil
	}
	return out[:2], nil
}

// Diff returns the full diff of dir against its base, including new files.
func (w *Worktree) Diff() (string, error) {
	// Mark untracked files as intent-to-add so they show up in the diff.
	if _, err := git(w.Dir, "add", "--intent-to-add", "--all"); err != nil {
		return "", err
	}
	return git(w.Dir, "diff", w.Base)
}

// Commit stages everything in the worktree and commits it on its branch.
// The author is fixed so the user's own identity is never used for agent work.
func (w *Worktree) Commit(message string) error {
	if _, err := git(w.Dir, "add", "--all"); err != nil {
		return err
	}
	_, err := git(w.Dir, "-c", "user.name=코드반장", "-c", "user.email=codebanjang@localhost",
		"commit", "--quiet", "-m", message)
	return err
}

// MergeInto merges branch into whatever is checked out in repo. On a conflict
// the merge is aborted so the user's folder is left as it was.
func MergeInto(repo, branch, message string) error {
	if _, err := git(repo, "merge", "--no-ff", "-m", message, branch); err != nil {
		git(repo, "merge", "--abort")
		return err
	}
	return nil
}

// Open describes an existing worktree from its stored fields.
func Open(repo, dir, branch, base string) *Worktree {
	return &Worktree{Repo: repo, Dir: dir, Branch: branch, Base: base}
}

// DiffBranch is the committed change of the branch against its base
// (works after the worktree is gone, as long as the branch exists).
func DiffBranch(repo, base, branch string) (string, error) {
	return git(repo, "diff", base+".."+branch)
}

// ChangedSinceBase lists files changed since the base commit, committed
// or not (new files included).
func (w *Worktree) ChangedSinceBase() ([]string, error) {
	git(w.Dir, "add", "--intent-to-add", "--all")
	out, err := git(w.Dir, "diff", "--name-only", w.Base)
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// DiffStat summarizes the branch against its base.
func (w *Worktree) DiffStat() (string, error) {
	return git(w.Dir, "diff", "--stat", w.Base)
}

// MatchAny reports whether path (slash-separated, relative) matches any of
// the glob patterns, tried against both the full path and the base name.
func MatchAny(patterns []string, path string) bool {
	base := path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		base = path[i+1:]
	}
	for _, p := range patterns {
		if ok, _ := filepath.Match(p, path); ok {
			return true
		}
		if ok, _ := filepath.Match(p, base); ok {
			return true
		}
	}
	return false
}

// Restore puts path back to its state at the base commit: tracked files are
// checked out from base, files that did not exist there are deleted.
func (w *Worktree) Restore(path string) error {
	if _, err := git(w.Dir, "cat-file", "-e", w.Base+":"+path); err == nil {
		_, err := git(w.Dir, "checkout", w.Base, "--", path)
		return err
	}
	// Not in base: drop any intent-to-add entry, then delete the file.
	git(w.Dir, "rm", "--cached", "--quiet", "--ignore-unmatch", "--", path)
	err := os.Remove(filepath.Join(w.Dir, filepath.FromSlash(path)))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Remove deletes the worktree and, if deleteBranch, its branch.
func (w *Worktree) Remove(deleteBranch bool) error {
	var errs []error
	if _, err := git(w.Repo, "worktree", "remove", "--force", w.Dir); err != nil {
		errs = append(errs, err)
	}
	if deleteBranch {
		if _, err := git(w.Repo, "branch", "-D", w.Branch); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
