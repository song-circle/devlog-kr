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
	args := append([]string{"log", "--topo-order", "--decorate=short", "-n", strconv.Itoa(n),
		"--format=%H%x1f%P%x1f%s%x1f%an%x1f%cI%x1f%D%x1e"}, have...)
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
