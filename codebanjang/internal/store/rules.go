package store

// Rule candidates: rules for a project that an AI proposed after reading
// what happened in a request (the person's follow-up answers, failed
// checks, review findings). A person accepts or rejects each one; accepted
// rules go into the project's knowledge document.

const ruleSchema = `
CREATE TABLE IF NOT EXISTS rule_candidates (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	project_id  INTEGER NOT NULL,
	request_id  TEXT NOT NULL,
	rule        TEXT NOT NULL,
	why         TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL DEFAULT 'pending', -- pending / accepted / rejected
	created_at  TEXT NOT NULL,
	decided_at  TEXT
);
`

// Rule statuses.
const (
	RulePending  = "pending"
	RuleAccepted = "accepted"
	RuleRejected = "rejected"
)

// RuleCandidate is one proposed rule.
type RuleCandidate struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"projectId"`
	RequestID string `json:"requestId"`
	Rule      string `json:"rule"`
	Why       string `json:"why"`
	Status    string `json:"status"`
	CreatedAt string `json:"createdAt"`
}

// AddRuleCandidate records a proposed rule.
func (s *Store) AddRuleCandidate(projectID int64, requestID, rule, why string) error {
	_, err := s.db.Exec(`INSERT INTO rule_candidates(project_id, request_id, rule, why, created_at) VALUES (?,?,?,?,?)`,
		projectID, requestID, rule, why, now())
	return err
}

// RuleCandidates lists a project's candidates with the given status ("" = all), newest first.
func (s *Store) RuleCandidates(projectID int64, status string) ([]RuleCandidate, error) {
	rows, err := s.db.Query(`SELECT id, project_id, request_id, rule, why, status, created_at FROM rule_candidates
		WHERE project_id=? AND (?='' OR status=?) ORDER BY id DESC`, projectID, status, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RuleCandidate{}
	for rows.Next() {
		var r RuleCandidate
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.RequestID, &r.Rule, &r.Why, &r.Status, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RuleCandidate returns one candidate.
func (s *Store) RuleCandidate(id int64) (RuleCandidate, error) {
	var r RuleCandidate
	err := s.db.QueryRow(`SELECT id, project_id, request_id, rule, why, status, created_at FROM rule_candidates WHERE id=?`, id).
		Scan(&r.ID, &r.ProjectID, &r.RequestID, &r.Rule, &r.Why, &r.Status, &r.CreatedAt)
	return r, err
}

// SetRuleStatus records the person's decision on a candidate (the rule
// text may have been edited before accepting).
func (s *Store) SetRuleStatus(id int64, status, rule string) error {
	_, err := s.db.Exec(`UPDATE rule_candidates SET status=?, rule=CASE WHEN ?='' THEN rule ELSE ? END, decided_at=? WHERE id=?`,
		status, rule, rule, now(), id)
	return err
}

// HasRuleCandidates reports whether rules were already proposed for a request.
func (s *Store) HasRuleCandidates(requestID string) bool {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM rule_candidates WHERE request_id=?`, requestID).Scan(&n)
	return n > 0
}
