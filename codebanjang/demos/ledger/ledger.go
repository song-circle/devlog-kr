// Package ledger reads a CSV of transactions and summarizes them.
// The rules are in SPEC.md.
package ledger

import "io"

// Txn is one valid transaction.
type Txn struct {
	Line    int    // file line where the record starts (1-based)
	Date    string // YYYY-MM-DD
	Account string
	Cents   int64
	Memo    string
}

// RowError describes one rejected record.
type RowError struct {
	Line int
	Msg  string
}

// MonthSummary totals one calendar month.
type MonthSummary struct {
	Month string // YYYY-MM
	In    int64  // sum of positive amounts
	Out   int64  // sum of negative amounts (negative)
	Net   int64  // In + Out
}

// Parse reads transactions. See SPEC.md.
func Parse(r io.Reader) ([]Txn, []RowError) {
	// TODO
	return nil, nil
}

// Balances sums cents per account.
func Balances(txns []Txn) map[string]int64 {
	// TODO
	return nil
}

// Monthly summarizes by month, ascending.
func Monthly(txns []Txn) []MonthSummary {
	// TODO
	return nil
}

// FormatCents renders cents like "-1,234.50".
func FormatCents(c int64) string {
	// TODO
	return ""
}
