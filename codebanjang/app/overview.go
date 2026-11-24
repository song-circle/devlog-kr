package main

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"strings"

	"github.com/song-circle/devlog-kr/codebanjang/internal/orchestrator"
	"github.com/song-circle/devlog-kr/codebanjang/internal/store"
	"github.com/song-circle/devlog-kr/codebanjang/internal/workspace"
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
	Me        string `json:"me"` // this PC's requester name

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
	Key   string      `json:"key"`
	Title string      `json:"title"`
	Hint  string      `json:"hint"`
	Items []BoardItem `json:"items"`
}

// BoardItem is one request on the board. Requests made on this PC come
// from its records (Local); others' requests come from the shared git
// history, where the work commit carries the request id and requester.
type BoardItem struct {
	ID         string  `json:"id"`
	Text       string  `json:"text"`
	Who        string  `json:"who"` // requester ("" = unknown)
	When       string  `json:"when"`
	CostUSD    float64 `json:"costUsd"`
	Reason     string  `json:"reason"`
	Local      bool    `json:"local"`      // has a request screen on this PC
	Commit     string  `json:"commit"`     // work commit
	DeployedBy string  `json:"deployedBy"` // who put it on dev (if recorded)
}

const (
	graphCommits   = 80
	historyCommits = 400 // how far back the shared board looks
	doneShown      = 12  // finished stages keep only the recent ones
)

// requesterName is who this PC's person is: the git user name of the
// project (what their own commits say), else the Windows user.
func requesterName(repo string) string {
	if n := workspace.ConfigValue(repo, "user.name"); n != "" {
		return n
	}
	if u, err := user.Current(); err == nil {
		name := u.Username
		if i := strings.LastIndexByte(name, '\\'); i >= 0 {
			name = name[i+1:]
		}
		return name
	}
	return ""
}

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
	ov := &Overview{ProjectID: projectID, Remote: target != "", CommitRequest: map[string]string{}, Me: requesterName(p.Repo)}
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

	cols := []Column{
		{Key: "running", Title: "작업 중", Hint: "AI가 일하는 중 (이 PC)"},
		{Key: "done", Title: "반영 대기", Hint: "검사 통과, 반영 버튼을 기다림 (이 PC)"},
	}
	if ov.Remote {
		cols = append(cols,
			Column{Key: "dev", Title: "개발 반영됨", Hint: ov.Dev + " 에 있음, 운영 대기 (모두)"},
			Column{Key: "prod", Title: "운영 반영됨", Hint: ov.Prod + " 까지 들어감 (모두, 최근)"})
	} else {
		cols = append(cols, Column{Key: "merged", Title: "반영됨", Hint: "이 PC 저장소에 merge함"})
	}
	cols = append(cols, Column{Key: "stopped", Title: "멈춤", Hint: "실패·중지·중단·변경 없음 (이 PC, 최근)"})
	col := map[string]*Column{}
	for i := range cols {
		cols[i].Items = []BoardItem{}
		col[cols[i].Key] = &cols[i]
	}
	add := func(key string, it BoardItem) {
		c := col[key]
		if c == nil {
			return
		}
		if (key == "prod" || key == "merged" || key == "stopped") && len(c.Items) >= doneShown {
			return
		}
		c.Items = append(c.Items, it)
	}

	// This PC's requests.
	reqs, err := a.st.ListRequestsIn([]string{p.Repo}, 300)
	if err != nil {
		return nil, err
	}
	running := map[string]bool{}
	for _, id := range a.Running() {
		running[id] = true
	}
	seen := map[string]bool{}
	var localDeployed []BoardItem // placed after the shared scan, newest first with the rest
	refs := []string{"HEAD"}
	if ov.Remote {
		refs = append(refs, ov.Prod, ov.Dev)
	}
	for _, r := range reqs {
		if orchestrator.ReadOnly(r.Mode) || r.Branch == "" {
			continue // questions, plans, reviews change nothing
		}
		it := BoardItem{ID: r.ID, Text: r.Text, Who: ov.Me, When: r.CreatedAt, CostUSD: r.CostUSD, Reason: r.Reason, Local: true}
		if r.Finished != "" {
			it.When = r.Finished
		}
		switch {
		case running[r.ID] || r.Status == store.ReqRunning:
			add("running", it)
		case r.Status == store.ReqDone:
			refs = append(refs, r.Branch)
			if c := workspace.Resolve(p.Repo, r.Branch); c != "" {
				ov.CommitRequest[c] = r.ID
			}
			add("done", it)
		case r.Status == store.ReqDev:
			it.Commit = a.st.WorkCommit(r.ID)
			if it.Commit != "" {
				ov.CommitRequest[it.Commit] = r.ID
			}
			if ov.Remote {
				localDeployed = append(localDeployed, it) // placed by the shared scan below
			} else {
				add("merged", it)
			}
		case r.Status == store.ReqApproved:
			if ov.Remote {
				add("dev", it) // merged locally before branches were set
			} else {
				add("merged", it)
			}
		case r.Status == store.ReqFailed || r.Status == store.ReqCancelled || r.Status == store.ReqInterrupted || r.Status == store.ReqNoChange:
			add("stopped", it)
		}
	}

	// Everyone's deployed requests, from the shared branch's history.
	if ov.Remote {
		local := map[string]BoardItem{}
		for _, it := range localDeployed {
			local[it.ID] = it
		}
		commits, err := workspace.RequestCommits(p.Repo, ov.Dev, historyCommits,
			orchestrator.TrailerRequest, orchestrator.TrailerRequester, orchestrator.TrailerDeployedBy)
		if err != nil {
			return nil, err
		}
		deployer := map[string]string{}
		for _, c := range commits {
			if c.DeployedBy != "" && deployer[c.Request] == "" {
				deployer[c.Request] = c.DeployedBy
			}
		}
		for _, c := range commits {
			if c.Requester == "" && c.DeployedBy != "" {
				continue // the merge commit of a deploy, not the work itself
			}
			if seen[c.Request] {
				continue
			}
			seen[c.Request] = true
			it, ok := local[c.Request]
			if !ok {
				it = BoardItem{ID: c.Request, Text: requestText(c), Who: c.Requester, When: c.Date}
			}
			it.Commit = c.Hash
			it.DeployedBy = deployer[c.Request]
			ov.CommitRequest[c.Hash] = c.Request
			if workspace.IsAncestor(p.Repo, c.Hash, ov.Prod) {
				add("prod", it)
			} else {
				add("dev", it)
			}
		}
		// Deployed here but not found in the history (older than the scan,
		// or dev was rewritten): keep them visible.
		for _, it := range localDeployed {
			if seen[it.ID] {
				continue
			}
			if it.Commit != "" && workspace.IsAncestor(p.Repo, it.Commit, ov.Prod) {
				add("prod", it)
			} else {
				add("dev", it)
			}
		}
	}

	ov.Columns = cols
	if ov.Commits, err = workspace.Log(p.Repo, graphCommits, refs); err != nil {
		return nil, err
	}
	for i, c := range ov.Commits {
		for j, ref := range c.Refs {
			ov.Commits[i].Refs[j] = strings.TrimPrefix(strings.TrimPrefix(ref, "cb/"), "aio/")
		}
	}
	return ov, nil
}

// requestText is the request as written, from the work commit's body
// (the subject is cut at 60 characters).
func requestText(c workspace.TrailerCommit) string {
	body := c.Body
	for _, k := range []string{orchestrator.TrailerRequest, orchestrator.TrailerRequester, orchestrator.TrailerMode, orchestrator.TrailerDeployedBy} {
		if i := strings.Index(body, "\n"+k+":"); i >= 0 {
			body = body[:i]
		} else if strings.HasPrefix(body, k+":") {
			body = ""
		}
	}
	if body = strings.TrimSpace(body); body != "" {
		return body
	}
	return strings.TrimPrefix(strings.TrimPrefix(c.Subject, "코드반장: "), "AI Office: ")
}
