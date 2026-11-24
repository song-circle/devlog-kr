package ledger

// Grading tests: never shown to the agents. aio-bench copies this file into
// the worktree only after the team has finished, runs it, then removes it.
// Every case follows SPEC.md, which the agents can read.

import (
	"strings"
	"testing"
)

func parse(s string) ([]Txn, []RowError) { return Parse(strings.NewReader(s)) }

func TestHiddenQuotedFields(t *testing.T) {
	txns, errs := parse("date,account,amount,memo\n" +
		"2026-03-01,cash,5,\"lunch, with \"\"Kim\"\"\"\n")
	if len(errs) != 0 || len(txns) != 1 || txns[0].Memo != `lunch, with "Kim"` {
		t.Fatalf("txns %+v errs %v", txns, errs)
	}
}

func TestHiddenMultilineMemoShiftsLines(t *testing.T) {
	txns, errs := parse("date,account,amount,memo\n" +
		"2026-03-01,cash,5,\"line one\nline two\nline three\"\n" +
		"2026-03-02,cash,oops,x\n" +
		"2026-03-03,cash,7,y\n")
	if len(errs) != 1 || errs[0].Line != 5 {
		t.Errorf("errs %v, want one error on line 5", errs)
	}
	if len(txns) != 2 || txns[0].Line != 2 || txns[1].Line != 6 {
		t.Errorf("txns %+v, want lines 2 and 6", txns)
	}
}

func TestHiddenAmountRules(t *testing.T) {
	good := map[string]int64{"0": 0, "-0": 0, "-0.00": 0, "7": 700, "7.5": 750, "-7.05": -705, " 12.30 ": 1230, "1000000": 100000000}
	for in, want := range good {
		txns, errs := parse("date,account,amount,memo\n2026-01-01,a," + in + ",m\n")
		if len(errs) != 0 || len(txns) != 1 || txns[0].Cents != want {
			t.Errorf("amount %q: txns %+v errs %v, want %d", in, txns, errs, want)
		}
	}
	bad := []string{"+5", "1,000", ".5", "5.", "1.234", "1e3", "--1", "", "-", "1 000", "0x10"}
	for _, in := range bad {
		row := "2026-01-01,a," + in + ",m"
		if strings.Contains(in, ",") {
			row = "2026-01-01,a,\"" + in + "\",m"
		}
		txns, errs := parse("date,account,amount,memo\n" + row + "\n")
		if len(errs) != 1 || len(txns) != 0 {
			t.Errorf("amount %q should be rejected: txns %+v errs %v", in, txns, errs)
		}
	}
}

func TestHiddenDates(t *testing.T) {
	ok := []string{"2024-02-29", "2026-12-31", "2000-02-29"}
	for _, d := range ok {
		if _, errs := parse("date,account,amount,memo\n" + d + ",a,1,m\n"); len(errs) != 0 {
			t.Errorf("date %s rejected: %v", d, errs)
		}
	}
	badDates := []string{"2023-02-29", "1900-02-29", "2026-13-01", "2026-04-31", "2026-1-05", "26-01-05", "2026/01/05"}
	for _, d := range badDates {
		if _, errs := parse("date,account,amount,memo\n" + d + ",a,1,m\n"); len(errs) != 1 {
			t.Errorf("date %s accepted", d)
		}
	}
}

func TestHiddenHeaderAndBlankLines(t *testing.T) {
	txns, errs := parse(" Date , ACCOUNT,Amount ,memo\n\n2026-01-01,a,1,m\n\n\n2026-01-02,b,2,n\n")
	if len(errs) != 0 || len(txns) != 2 || txns[1].Line != 6 {
		t.Errorf("txns %+v errs %v", txns, errs)
	}
	txns, errs = parse("when,who,how much,why\n2026-01-01,a,1,m\n")
	if len(txns) != 0 || len(errs) != 1 || errs[0].Line != 1 || errs[0].Msg != "bad header" {
		t.Errorf("bad header: txns %+v errs %v", txns, errs)
	}
}

func TestHiddenFieldCountAndEmptyAccount(t *testing.T) {
	txns, errs := parse("date,account,amount,memo\n2026-01-01,a,1\n2026-01-01,  ,1,m\n2026-01-01,a,1,m,extra\n2026-01-02,b,3,ok\n")
	if len(errs) != 3 || len(txns) != 1 || txns[0].Account != "b" {
		t.Errorf("txns %+v errs %v", txns, errs)
	}
	if len(errs) == 3 && (errs[0].Line != 2 || errs[1].Line != 3 || errs[2].Line != 4) {
		t.Errorf("error lines %v", errs)
	}
}

func TestHiddenMonthlyAcrossYears(t *testing.T) {
	txns, _ := parse("date,account,amount,memo\n" +
		"2026-01-10,a,10,x\n2025-12-31,a,-4,x\n2026-01-11,b,-1.5,x\n2025-12-01,a,3,x\n")
	m := Monthly(txns)
	want := []MonthSummary{{"2025-12", 300, -400, -100}, {"2026-01", 1000, -150, 850}}
	if len(m) != 2 || m[0] != want[0] || m[1] != want[1] {
		t.Errorf("monthly %v, want %v", m, want)
	}
	if len(Monthly(nil)) != 0 {
		t.Error("Monthly(nil) should be empty")
	}
}

func TestHiddenFormatCents(t *testing.T) {
	cases := map[int64]string{-5: "-0.05", 5: "0.05", -100000000: "-1,000,000.00", 100: "1.00", 99999: "999.99",
		100000: "1,000.00", -123456789: "-1,234,567.89"}
	for c, want := range cases {
		if got := FormatCents(c); got != want {
			t.Errorf("FormatCents(%d) = %q, want %q", c, got, want)
		}
	}
}
