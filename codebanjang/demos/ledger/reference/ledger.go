// Reference solution used only to validate the hidden tests
// (aio-bench never copies this folder into the agents' repository).
package ledger

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Txn struct {
	Line    int
	Date    string
	Account string
	Cents   int64
	Memo    string
}

type RowError struct {
	Line int
	Msg  string
}

type MonthSummary struct {
	Month string
	In    int64
	Out   int64
	Net   int64
}

var (
	dateRe   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	amountRe = regexp.MustCompile(`^(-?)(\d+)(?:\.(\d{1,2}))?$`)
)

func Parse(r io.Reader) ([]Txn, []RowError) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	var txns []Txn
	var errs []RowError

	head, err := cr.Read()
	want := []string{"date", "account", "amount", "memo"}
	if err != nil || len(head) != 4 {
		return nil, []RowError{{1, "bad header"}}
	}
	for i := range head {
		if strings.ToLower(strings.TrimSpace(head[i])) != want[i] {
			return nil, []RowError{{1, "bad header"}}
		}
	}
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line, _ := cr.FieldPos(0)
		if err != nil {
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				line = pe.StartLine
			}
			errs = append(errs, RowError{line, err.Error()})
			continue
		}
		if len(rec) != 4 {
			errs = append(errs, RowError{line, fmt.Sprintf("want 4 fields, got %d", len(rec))})
			continue
		}
		for i := range rec {
			rec[i] = strings.TrimSpace(rec[i])
		}
		if !dateRe.MatchString(rec[0]) {
			errs = append(errs, RowError{line, "bad date"})
			continue
		}
		if _, err := time.Parse("2006-01-02", rec[0]); err != nil {
			errs = append(errs, RowError{line, "bad date"})
			continue
		}
		if rec[1] == "" {
			errs = append(errs, RowError{line, "empty account"})
			continue
		}
		m := amountRe.FindStringSubmatch(rec[2])
		if m == nil {
			errs = append(errs, RowError{line, "bad amount"})
			continue
		}
		whole, err := strconv.ParseInt(m[2], 10, 64)
		if err != nil || whole > 1e15 {
			errs = append(errs, RowError{line, "bad amount"})
			continue
		}
		frac := m[3]
		for len(frac) < 2 {
			frac += "0"
		}
		f, _ := strconv.ParseInt(frac, 10, 64)
		c := whole*100 + f
		if m[1] == "-" {
			c = -c
		}
		txns = append(txns, Txn{line, rec[0], rec[1], c, rec[3]})
	}
	return txns, errs
}

func Balances(txns []Txn) map[string]int64 {
	b := map[string]int64{}
	for _, t := range txns {
		b[t.Account] += t.Cents
	}
	return b
}

func Monthly(txns []Txn) []MonthSummary {
	by := map[string]*MonthSummary{}
	for _, t := range txns {
		k := t.Date[:7]
		s := by[k]
		if s == nil {
			s = &MonthSummary{Month: k}
			by[k] = s
		}
		if t.Cents >= 0 {
			s.In += t.Cents
		} else {
			s.Out += t.Cents
		}
		s.Net = s.In + s.Out
	}
	out := make([]MonthSummary, 0, len(by))
	for _, s := range by {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Month < out[j].Month })
	return out
}

func FormatCents(c int64) string {
	neg := c < 0
	u := uint64(c)
	if neg {
		u = uint64(-c)
	}
	whole := strconv.FormatUint(u/100, 10)
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	for i, ch := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	fmt.Fprintf(&b, ".%02d", u%100)
	return b.String()
}
