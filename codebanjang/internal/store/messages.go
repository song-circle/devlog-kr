package store

// Structured messages between employees (원본 기획 4.3): every statement in
// a meeting is one row, with who said it, to whom, of what kind, and the
// structured data behind it.

const messageSchema = `
CREATE TABLE IF NOT EXISTS agent_messages (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	request_id  TEXT NOT NULL REFERENCES requests(id),
	task_id     INTEGER REFERENCES tasks(id),
	meeting     TEXT NOT NULL,          -- cross-review / draft-critique / discussion
	round       INTEGER NOT NULL,
	sender      TEXT NOT NULL,          -- employee name
	receiver    TEXT NOT NULL DEFAULT '', -- '' = everyone in the meeting
	kind        TEXT NOT NULL,          -- finding / verdict / draft / critique / revision / opinion / conclusion
	content     TEXT NOT NULL,          -- readable text
	data_json   TEXT NOT NULL DEFAULT '{}',
	cost_usd    REAL NOT NULL DEFAULT 0,
	at          TEXT NOT NULL
);
`

// Message is one statement in a meeting.
type Message struct {
	ID        int64   `json:"id"`
	RequestID string  `json:"requestId"`
	TaskID    int64   `json:"taskId"`
	Meeting   string  `json:"meeting"`
	Round     int     `json:"round"`
	Sender    string  `json:"sender"`
	Receiver  string  `json:"receiver"`
	Kind      string  `json:"kind"`
	Content   string  `json:"content"`
	DataJSON  string  `json:"data"`
	CostUSD   float64 `json:"costUsd"`
	At        string  `json:"at"`
}

func (s *Store) AddMessage(m Message) (int64, error) {
	if m.DataJSON == "" {
		m.DataJSON = "{}"
	}
	res, err := s.db.Exec(`INSERT INTO agent_messages(request_id, task_id, meeting, round, sender, receiver, kind, content, data_json, cost_usd, at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`, m.RequestID, nullID(m.TaskID), m.Meeting, m.Round, m.Sender, m.Receiver, m.Kind,
		m.Content, m.DataJSON, m.CostUSD, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Messages returns a request's messages in order.
func (s *Store) Messages(reqID string) ([]Message, error) {
	rows, err := s.db.Query(`SELECT id, request_id, COALESCE(task_id,0), meeting, round, sender, receiver, kind, content, data_json, cost_usd, at
		FROM agent_messages WHERE request_id=? ORDER BY id`, reqID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.RequestID, &m.TaskID, &m.Meeting, &m.Round, &m.Sender, &m.Receiver, &m.Kind,
			&m.Content, &m.DataJSON, &m.CostUSD, &m.At); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
