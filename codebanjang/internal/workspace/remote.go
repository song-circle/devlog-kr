package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Fetch updates the remote-tracking branch remote/branch in repo.
func Fetch(repo, remote, branch string) error {
	_, err := git(repo, "fetch", "--quiet", remote, "+refs/heads/"+branch+":refs/remotes/"+remote+"/"+branch)
	return err
}

// CreateFrom is Create starting from base (a commit or ref such as
// "origin/dev") instead of the project folder's HEAD.
func CreateFrom(repo, root, id, base string) (*Worktree, error) {
	top, err := TopLevel(repo)
	if err != nil {
		return nil, err
	}
	commit, err := git(top, "rev-parse", "--verify", base+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("%s 를 찾을 수 없습니다: %w", base, err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	w := &Worktree{Repo: top, Dir: filepath.Join(root, id), Branch: "cb/" + id, Base: strings.TrimSpace(commit)}
	if _, err := git(top, "worktree", "add", "-b", w.Branch, w.Dir, w.Base); err != nil {
		return nil, err
	}
	return w, nil
}

// ErrConflict means the latest target branch conflicts with the request.
var ErrConflict = errors.New("conflict")

// MergeLatest merges ref (the latest target branch) into the worktree's
// branch so the request sits on top of what others have merged meanwhile.
// On a conflict the merge is aborted and ErrConflict returned with the
// conflicting paths. Returns whether anything new came in.
func (w *Worktree) MergeLatest(ref, message string) (bool, error) {
	before, _ := git(w.Dir, "rev-parse", "HEAD")
	if _, err := git(w.Dir, "-c", "user.name=코드반장", "-c", "user.email=codebanjang@localhost",
		"merge", "--no-edit", "-m", message, ref); err != nil {
		files, _ := git(w.Dir, "diff", "--name-only", "--diff-filter=U")
		git(w.Dir, "merge", "--abort")
		if f := strings.Fields(files); len(f) > 0 {
			return false, fmt.Errorf("%w: %s", ErrConflict, strings.Join(f, ", "))
		}
		return false, err
	}
	after, _ := git(w.Dir, "rev-parse", "HEAD")
	return before != after, nil
}

// ErrRejected means the remote branch moved since it was fetched.
var ErrRejected = errors.New("rejected")

// PushTo pushes the worktree's HEAD to remote/branch. It never forces:
// when someone pushed in between, ErrRejected comes back and the caller
// merges the newer branch and checks again.
func (w *Worktree) PushTo(remote, branch string) error {
	if _, err := git(w.Dir, "push", "--quiet", remote, "HEAD:refs/heads/"+branch); err != nil {
		if s := err.Error(); strings.Contains(s, "rejected") || strings.Contains(s, "fetch first") || strings.Contains(s, "non-fast-forward") {
			return fmt.Errorf("%w: %v", ErrRejected, err)
		}
		return err
	}
	return nil
}

// Head is the commit the worktree is on.
func (w *Worktree) Head() string {
	out, _ := git(w.Dir, "rev-parse", "HEAD")
	return strings.TrimSpace(out)
}

// Subjects lists commit subjects in from..to (newest first), e.g. what a
// release would bring from dev to main.
func Subjects(repo, from, to string) ([]string, error) {
	out, err := git(repo, "log", "--no-merges", "--format=%s", from+".."+to)
	if err != nil {
		return nil, err
	}
	var s []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			s = append(s, l)
		}
	}
	return s, nil
}
