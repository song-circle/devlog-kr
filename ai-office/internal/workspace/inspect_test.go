package workspace

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLogAndCounts(t *testing.T) {
	repo := newRepo(t) // main with one commit
	main := CurrentBranch(repo)
	run(t, repo, "git", "checkout", "-q", "-b", "dev")
	write(t, filepath.Join(repo, "a.txt"), "a\n")
	run(t, repo, "git", "add", "-A")
	run(t, repo, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "add a")
	run(t, repo, "git", "checkout", "-q", main)
	run(t, repo, "git", "-c", "user.name=t", "-c", "user.email=t@t", "merge", "-q", "--no-ff", "-m", "merge dev", "dev")

	if n := Ahead(repo, "dev", main); n != 0 {
		t.Errorf("merge commit counted: %d", n)
	}
	a := Resolve(repo, "dev")
	if !IsAncestor(repo, a, main) || IsAncestor(repo, Resolve(repo, main), "dev") {
		t.Errorf("IsAncestor wrong")
	}
	log, err := Log(repo, 10, []string{main, "dev", "no-such-branch"})
	if err != nil || len(log) != 3 {
		t.Fatalf("log %d, %v", len(log), err)
	}
	if log[0].Subject != "merge dev" || len(log[0].Parents) != 2 || strings.Join(log[0].Refs, ",") != main {
		t.Errorf("first = %+v", log[0])
	}
	if log[1].Subject != "add a" || strings.Join(log[1].Refs, ",") != "dev" {
		t.Errorf("second = %+v", log[1])
	}
}
