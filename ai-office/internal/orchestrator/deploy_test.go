package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/song-circle/devlog-kr/ai-office/internal/store"
	"github.com/song-circle/devlog-kr/ai-office/internal/workspace"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// shared sets up a GitHub stand-in: a bare "origin" with main and dev, the
// project folder cloned from it, and a colleague's clone.
func shared(t *testing.T) (origin, project, colleague string) {
	origin = filepath.Join(t.TempDir(), "origin.git")
	gitT(t, ".", "init", "-q", "--bare", "-b", "main", origin)
	seed := t.TempDir()
	gitT(t, seed, "clone", "-q", origin, ".")
	write(t, seed, "app.txt", "line1\n")
	gitT(t, seed, "add", "-A")
	gitT(t, seed, "commit", "-qm", "start")
	gitT(t, seed, "push", "-q", "origin", "HEAD:main", "HEAD:dev")
	project, colleague = t.TempDir(), t.TempDir()
	gitT(t, project, "clone", "-q", origin, ".")
	gitT(t, colleague, "clone", "-q", "-b", "dev", origin, ".")
	return
}

// a request made on origin/dev with one new file.
func requestOnDev(t *testing.T, project, data, name string) *workspace.Worktree {
	t.Helper()
	if err := workspace.Fetch(project, "origin", "dev"); err != nil {
		t.Fatal(err)
	}
	wt, err := workspace.CreateFrom(project, filepath.Join(data, "worktrees"), "r-"+name, "origin/dev")
	if err != nil {
		t.Fatal(err)
	}
	write(t, wt.Dir, name, "from request\n")
	if err := wt.Commit("request " + name); err != nil {
		t.Fatal(err)
	}
	return wt
}

func TestDeployMergesLatestAndPushes(t *testing.T) {
	origin, project, colleague := shared(t)
	data := t.TempDir()
	st, _ := store.Open(filepath.Join(data, "aio.db"))
	defer st.Close()
	wt := requestOnDev(t, project, data, "mine.txt")
	st.CreateRequest(store.Request{ID: "r-mine.txt", Text: "x", Repo: project, Worktree: wt.Dir, Branch: wt.Branch, Base: wt.Base})

	// Meanwhile a colleague deploys something else to dev.
	write(t, colleague, "theirs.txt", "colleague\n")
	gitT(t, colleague, "add", "-A")
	gitT(t, colleague, "commit", "-qm", "colleague change")
	gitT(t, colleague, "push", "-q", "origin", "dev")

	var lines []string
	o := &Orchestrator{Store: st, DataDir: data, Log: func(r, l string) { lines = append(lines, l) }}
	// The check needs both changes: it runs on the combined code.
	head, err := o.DeployTo(context.Background(), Deploy{RequestID: "r-mine.txt", Worktree: wt, Remote: "origin", Target: "dev",
		Checks: []string{`findstr colleague theirs.txt && findstr request mine.txt`}})
	if err != nil {
		t.Fatalf("%v\n%s", err, strings.Join(lines, "\n"))
	}
	if got := gitT(t, origin, "rev-parse", "dev"); got != head {
		t.Errorf("origin dev %s, want %s", got, head)
	}
	files := gitT(t, origin, "ls-tree", "--name-only", "dev")
	if !strings.Contains(files, "mine.txt") || !strings.Contains(files, "theirs.txt") {
		t.Errorf("dev has %q", files)
	}
	if got := gitT(t, origin, "rev-parse", "main"); got == head {
		t.Errorf("main moved")
	}
	var n int
	st.DB().QueryRow(`SELECT COUNT(*) FROM checks WHERE request_id='r-mine.txt' AND round>100`).Scan(&n)
	if n != 1 {
		t.Errorf("deploy checks recorded = %d", n)
	}
	subj, _ := workspace.Subjects(project, "origin/main", "origin/dev")
	if strings.Join(subj, ",") != "request mine.txt,colleague change" && strings.Join(subj, ",") != "colleague change,request mine.txt" {
		t.Errorf("release subjects %v", subj)
	}
}

func TestDeployStopsOnConflictOrFailedCheck(t *testing.T) {
	origin, project, colleague := shared(t)
	data := t.TempDir()
	st, _ := store.Open(filepath.Join(data, "aio.db"))
	defer st.Close()
	o := &Orchestrator{Store: st, DataDir: data}

	// Conflict: both change app.txt.
	wt, _ := workspace.CreateFrom(project, filepath.Join(data, "worktrees"), "r1", "origin/dev")
	write(t, wt.Dir, "app.txt", "mine\n")
	wt.Commit("mine")
	write(t, colleague, "app.txt", "theirs\n")
	gitT(t, colleague, "commit", "-qam", "theirs")
	gitT(t, colleague, "push", "-q", "origin", "dev")
	before := gitT(t, origin, "rev-parse", "dev")
	_, err := o.DeployTo(context.Background(), Deploy{RequestID: "r1", Worktree: wt, Remote: "origin", Target: "dev"})
	if err == nil || !strings.Contains(err.Error(), "충돌") || !strings.Contains(err.Error(), "app.txt") {
		t.Errorf("conflict err = %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(wt.Dir, "app.txt")); strings.TrimSpace(string(b)) != "mine" {
		t.Errorf("work folder left mid-merge: %q", b)
	}

	// Failed check on the combined code: nothing is pushed.
	wt2 := requestOnDev(t, project, data, "x.txt")
	_, err = o.DeployTo(context.Background(), Deploy{RequestID: "r2", Worktree: wt2, Remote: "origin", Target: "dev", Checks: []string{"exit 1"}})
	if err == nil || !strings.Contains(err.Error(), "반영하지 않았습니다") {
		t.Errorf("check err = %v", err)
	}
	if got := gitT(t, origin, "rev-parse", "dev"); got != before {
		t.Errorf("dev moved after failures")
	}
}

// Whose request a change was travels with the commits: a colleague who
// fetches dev finds the request id, the requester and who deployed it.
func TestRequestInfoInCommits(t *testing.T) {
	origin, project, colleague := shared(t)
	data := t.TempDir()
	st, _ := store.Open(filepath.Join(data, "aio.db"))
	defer st.Close()
	workspace.Fetch(project, "origin", "dev")
	wt, _ := workspace.CreateFrom(project, filepath.Join(data, "worktrees"), "20261124-120000-001", "origin/dev")
	write(t, wt.Dir, "x.txt", "x\n")
	if err := wt.Commit(workMessage("20261124-120000-001", "김민지", ModeSolo, "주문 목록에 엑셀 버튼을 추가해 줘.\n파일 이름은 날짜로.")); err != nil {
		t.Fatal(err)
	}
	// Someone else deployed first, so the deploy makes a merge commit too.
	write(t, colleague, "y.txt", "y\n")
	gitT(t, colleague, "add", "-A")
	gitT(t, colleague, "commit", "-qm", "other")
	gitT(t, colleague, "push", "-q", "origin", "dev")
	o := &Orchestrator{Store: st, DataDir: data}
	if _, err := o.DeployTo(context.Background(), Deploy{RequestID: "20261124-120000-001", Worktree: wt, Remote: "origin", Target: "dev", Deployer: "박팀장"}); err != nil {
		t.Fatal(err)
	}
	_ = origin
	gitT(t, colleague, "fetch", "-q")
	cs, err := workspace.RequestCommits(colleague, "origin/dev", 50, TrailerRequest, TrailerRequester, TrailerDeployedBy)
	if err != nil || len(cs) != 2 {
		t.Fatalf("commits %+v, %v", cs, err)
	}
	merge, work := cs[0], cs[1]
	if merge.Request != "20261124-120000-001" || merge.DeployedBy != "박팀장" || merge.Requester != "" {
		t.Errorf("merge commit %+v", merge)
	}
	if work.Request != "20261124-120000-001" || work.Requester != "김민지" || !strings.Contains(work.Body, "파일 이름은 날짜로.") {
		t.Errorf("work commit %+v", work)
	}
}
