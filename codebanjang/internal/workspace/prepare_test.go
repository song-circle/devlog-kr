package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, path, text string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newRepo(t *testing.T) string {
	repo := t.TempDir()
	run(t, repo, "git", "init", "-q")
	write(t, filepath.Join(repo, ".gitignore"), ".env\nconfig/local/\n")
	write(t, filepath.Join(repo, "main.go"), "package main\n")
	run(t, repo, "git", "add", "-A")
	run(t, repo, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "start")
	write(t, filepath.Join(repo, ".env"), "KEY=1\n")
	write(t, filepath.Join(repo, "config", "local", "a.json"), "{}")
	write(t, filepath.Join(repo, "notes.txt"), "not ignored\n")
	return repo
}

func TestCopyIgnored(t *testing.T) {
	repo := newRepo(t)
	wt, err := Create(repo, t.TempDir(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	copied, missing, err := CopyIgnored(repo, wt.Dir, []string{".env", "config/local", ".env.local"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(copied, ",") != ".env,config/local" || strings.Join(missing, ",") != ".env.local" {
		t.Errorf("copied %v missing %v", copied, missing)
	}
	if b, _ := os.ReadFile(filepath.Join(wt.Dir, "config", "local", "a.json")); string(b) != "{}" {
		t.Errorf("folder not copied")
	}
	// Copied files are ignored: they never reach the request's changes.
	if ch, _ := Status(wt.Dir); len(ch) != 0 {
		t.Errorf("work folder shows changes: %v", ch)
	}
}

func TestCopyRefusesFilesGitSees(t *testing.T) {
	repo := newRepo(t)
	wt, _ := Create(repo, t.TempDir(), "r1")
	for _, pat := range []string{"notes.txt", "main.go"} {
		if _, _, err := CopyIgnored(repo, wt.Dir, []string{pat}); err == nil || !strings.Contains(err.Error(), ".gitignore") {
			t.Errorf("%s: err = %v", pat, err)
		}
	}
	if _, _, err := CopyIgnored(repo, wt.Dir, []string{"../x"}); err == nil {
		t.Errorf("path outside the project accepted")
	}
}
