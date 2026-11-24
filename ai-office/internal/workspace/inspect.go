package workspace

import (
	"strconv"
	"strings"
)

// Read-only questions about a repository, for the progress screen.

// CurrentBranch is the branch checked out in dir ("" when detached).
func CurrentBranch(dir string) string {
	out, err := git(dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// RefExists reports whether ref names a commit.
func RefExists(repo, ref string) bool {
	_, err := git(repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil
}

// Resolve returns the commit a ref points to ("" if none).
func Resolve(repo, ref string) string {
	out, err := git(repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// Ahead counts commits in to that are not in from (from..to), merges
// left out: they are bookkeeping, not changes.
func Ahead(repo, from, to string) int {
	out, err := git(repo, "rev-list", "--count", "--no-merges", from+".."+to)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(out))
	return n
}

// IsAncestor reports whether commit is contained in ref.
func IsAncestor(repo, commit, ref string) bool {
	_, err := git(repo, "merge-base", "--is-ancestor", commit, ref)
	return err == nil
}

// LastCommitTime is the commit date of ref (RFC 3339, "" if none).
func LastCommitTime(repo, ref string) string {
	out, err := git(repo, "log", "-1", "--format=%cI", ref)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// Commit is one node of the history graph.
type Commit struct {
	Hash    string   `json:"hash"`
	Parents []string `json:"parents"`
	Subject string   `json:"subject"`
	Author  string   `json:"author"`
	Date    string   `json:"date"` // RFC 3339
	Refs    []string `json:"refs"` // branch names pointing here
}

// Log lists up to n commits reachable from refs, children before parents
// (the order a graph is drawn in). Refs that do not exist are skipped.
// Only the given refs label commits (other branches would be noise).
func Log(repo string, n int, refs []string) ([]Commit, error) {
	var have []string
	for _, r := range refs {
		if RefExists(repo, r) {
			have = append(have, r)
		}
	}
	if len(have) == 0 {
		return []Commit{}, nil
	}
	args := []string{"log", "--topo-order", "--decorate=short", "-n", strconv.Itoa(n),
		"--format=%H%x1f%P%x1f%s%x1f%an%x1f%cI%x1f%D%x1e"}
	for _, r := range have {
		full := r
		if r == "HEAD" {
			if b := CurrentBranch(repo); b != "" {
				full = "refs/heads/" + b
			}
		} else if out, err := git(repo, "rev-parse", "--symbolic-full-name", r); err == nil && strings.TrimSpace(out) != "" {
			full = strings.TrimSpace(out)
		}
		args = append(args, "--decorate-refs="+full)
	}
	args = append(args, have...)
	out, err := git(repo, args...)
	if err != nil {
		return nil, err
	}
	commits := []Commit{}
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimLeft(rec, "\r\n")
		f := strings.Split(rec, "\x1f")
		if len(f) < 6 {
			continue
		}
		c := Commit{Hash: f[0], Parents: strings.Fields(f[1]), Subject: f[2], Author: f[3], Date: f[4], Refs: []string{}}
		for _, r := range strings.Split(f[5], ",") {
			r = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(r), "HEAD -> "))
			if r != "" && r != "HEAD" && !strings.HasPrefix(r, "tag: ") && !strings.HasSuffix(r, "/HEAD") {
				c.Refs = append(c.Refs, r)
			}
		}
		commits = append(commits, c)
	}
	return commits, nil
}

// TrailerCommit is a commit carrying AI Office request trailers.
type TrailerCommit struct {
	Hash       string
	Date       string // RFC 3339
	Subject    string
	Body       string
	Request    string // request id
	Requester  string
	DeployedBy string
}

// RequestCommits finds commits reachable from ref (up to n, newest first)
// that name a request in their trailers.
func RequestCommits(repo, ref string, n int, keyRequest, keyRequester, keyDeployed string) ([]TrailerCommit, error) {
	if !RefExists(repo, ref) {
		return nil, nil
	}
	tr := func(k string) string { return "%(trailers:key=" + k + ",valueonly,separator=%x2C)" }
	out, err := git(repo, "log", "-n", strconv.Itoa(n),
		"--format=%H%x1f%cI%x1f%s%x1f%b%x1f"+tr(keyRequest)+"%x1f"+tr(keyRequester)+"%x1f"+tr(keyDeployed)+"%x1e", ref)
	if err != nil {
		return nil, err
	}
	var cs []TrailerCommit
	for _, rec := range strings.Split(out, "\x1e") {
		f := strings.Split(strings.TrimLeft(rec, "\r\n"), "\x1f")
		if len(f) < 7 {
			continue
		}
		c := TrailerCommit{Hash: f[0], Date: f[1], Subject: f[2], Body: f[3],
			Request: strings.TrimSpace(f[4]), Requester: strings.TrimSpace(f[5]), DeployedBy: strings.TrimSpace(f[6])}
		if c.Request != "" {
			cs = append(cs, c)
		}
	}
	return cs, nil
}

// ConfigValue reads a git setting as seen from repo ("" if unset).
func ConfigValue(repo, key string) string {
	out, err := git(repo, "config", "--get", key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}
