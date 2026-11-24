package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/song-circle/devlog-kr/ai-office/internal/orchestrator"
	"github.com/song-circle/devlog-kr/ai-office/internal/store"
	"github.com/song-circle/devlog-kr/ai-office/internal/workspace"
)

// Overview is the progress screen of one folder: where production, the
// development branch and this PC stand, every request on a board by how
// far it got, and the history graph.
type Overview struct {
	ProjectID int64  `json:"projectId"`
	Remote    bool   `json:"remote"` // has dev / prod branches on origin
	Dev       string `json:"dev"`    // "origin/dev" (remote projects)
	Prod      string `json:"prod"`   // "origin/main"
	FetchErr  string `json:"fetchErr"`

	ProdTime    string `json:"prodTime"`    // last commit on prod
	DevAhead    int    `json:"devAhead"`    // commits on dev waiting for production
	LocalBr     string `json:"localBranch"` // checked out on this PC
	LocalDirty  int    `json:"localDirty"`  // changed files not committed on this PC
	LocalBehind int    `json:"localBehind"` // commits on dev (or nothing) this PC lacks
	LocalAhead  int    `json:"localAhead"`  // commits on this PC not on dev

	Columns []Column           `json:"columns"`
	Commits []workspace.Commit `json:"commits"`
	// CommitRequest maps a commit to the request that made it.
	CommitRequest map[string]string `json:"commitRequest"`
}

// Column is one stage of the board.
type Column struct {
	Key   string             `json:"key"`
	Title string             `json:"title"`
	Hint  string             `json:"hint"`
	Items []store.RequestRow `json:"items"`
}

const graphCommits = 80

// Overview builds the progress screen. fetch=true first updates the shared
// branches from GitHub (slower, needs the network).
func (a *App) Overview(projectID int64, fetch bool) (*Overview, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	if !a.inWorkspace(projectID) {
		return nil, errors.New("이 작업공간의 폴더가 아닙니다")
	}
	p, target := a.deployTarget(projectID)
	if _, err := os.Stat(p.Repo); err != nil {
		return nil, fmt.Errorf("폴더가 없습니다: %s", p.Repo)
	}
	ov := &Overview{ProjectID: projectID, Remote: target != "", CommitRequest: map[string]string{}}
	if ov.Remote {
		ov.Dev, ov.Prod = target, remote+"/"+p.ProdBranch
		if fetch {
			for _, b := range []string{p.DevBranch, p.ProdBranch} {
				if err := workspace.Fetch(p.Repo, remote, b); err != nil {
					ov.FetchErr = fmt.Sprintf("GitHub에서 %s/%s 를 가져오지 못했습니다(이전에 받은 상태로 표시): %v", remote, b, err)
				}
			}
		}
		ov.ProdTime = workspace.LastCommitTime(p.Repo, ov.Prod)
		ov.DevAhead = workspace.Ahead(p.Repo, ov.Prod, ov.Dev)
	}
	ov.LocalBr = workspace.CurrentBranch(p.Repo)
	if ch, err := workspace.Status(p.Repo); err == nil {
		ov.LocalDirty = len(ch)
	}
	if ov.Remote {
		ov.LocalBehind = workspace.Ahead(p.Repo, "HEAD", ov.Dev)
		ov.LocalAhead = workspace.Ahead(p.Repo, ov.Dev, "HEAD")
	}

	reqs, err := a.st.ListRequestsIn([]string{p.Repo}, 300)
	if err != nil {
		return nil, err
	}
	running := map[string]bool{}
	for _, id := range a.Running() {
		running[id] = true
	}
	cols := []Column{
		{Key: "running", Title: "작업 중", Hint: "AI가 일하는 중"},
		{Key: "done", Title: "반영 대기", Hint: "검사 통과, 반영 버튼을 기다림"},
	}
	if ov.Remote {
		cols = append(cols,
			Column{Key: "dev", Title: "개발 반영됨", Hint: ov.Dev + " 에 있음, 운영 대기"},
			Column{Key: "prod", Title: "운영 반영됨", Hint: ov.Prod + " 까지 들어감"})
	} else {
		cols = append(cols, Column{Key: "merged", Title: "반영됨", Hint: "이 PC 저장소에 merge함"})
	}
	cols = append(cols, Column{Key: "stopped", Title: "멈춤", Hint: "실패·중지·중단 (최근)"})
	col := map[string]*Column{}
	for i := range cols {
		cols[i].Items = []store.RequestRow{}
		col[cols[i].Key] = &cols[i]
	}
	refs := []string{"HEAD"}
	if ov.Remote {
		refs = append(refs, ov.Prod, ov.Dev)
	}
	for _, r := range reqs {
		if orchestrator.ReadOnly(r.Mode) || r.Branch == "" {
			continue // questions, plans, reviews change nothing
		}
		key := ""
		switch {
		case running[r.ID] || r.Status == store.ReqRunning:
			key = "running"
		case r.Status == store.ReqDone:
			key = "done"
			refs = append(refs, r.Branch)
			if c := workspace.Resolve(p.Repo, r.Branch); c != "" {
				ov.CommitRequest[c] = r.ID
			}
		case r.Status == store.ReqDev:
			key = "dev"
			if work := a.st.WorkCommit(r.ID); work != "" {
				ov.CommitRequest[work] = r.ID
				if ov.Remote && workspace.IsAncestor(p.Repo, work, ov.Prod) {
					key = "prod"
				}
			}
		case r.Status == store.ReqApproved:
			key = "merged"
			if ov.Remote {
				key = "dev" // merged locally before branches were set
			}
		case r.Status == store.ReqFailed || r.Status == store.ReqCancelled || r.Status == store.ReqInterrupted:
			key = "stopped"
		}
		if c := col[key]; c != nil {
			// Finished stages keep only the recent ones on the board.
			if (key == "prod" || key == "merged" || key == "stopped") && len(c.Items) >= 12 {
				continue
			}
			c.Items = append(c.Items, r)
		}
	}
	ov.Columns = cols
	if ov.Commits, err = workspace.Log(p.Repo, graphCommits, refs); err != nil {
		return nil, err
	}
	// Name request branches by their request in the graph labels.
	for i, c := range ov.Commits {
		for j, ref := range c.Refs {
			ov.Commits[i].Refs[j] = strings.TrimPrefix(ref, "aio/")
		}
	}
	return ov, nil
}
