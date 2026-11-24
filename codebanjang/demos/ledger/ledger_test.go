package ledger

import (
	"strings"
	"testing"
)

const basic = `date,account,amount,memo
2026-01-05,cash,100.00,salary
2026-01-20,cash,-30.5,food
2026-02-01,card,-12.34,book
`

func TestParseBasic(t *testing.T) {
	txns, errs := Parse(strings.NewReader(basic))
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(txns) != 3 {
		t.Fatalf("got %d txns", len(txns))
	}
	want := Txn{Line: 3, Date: "2026-01-20", Account: "cash", Cents: -3050, Memo: "food"}
	if txns[1] != want {
		t.Errorf("txns[1] = %+v, want %+v", txns[1], want)
	}
}

func TestBadAmountIsReportedAndSkipped(t *testing.T) {
	txns, errs := Parse(strings.NewReader("date,account,amount,memo\n2026-01-01,cash,abc,x\n2026-01-02,cash,1,y\n"))
	if len(txns) != 1 || len(errs) != 1 || errs[0].Line != 2 {
		t.Fatalf("txns %v errs %v", txns, errs)
	}
}

func TestBalancesAndMonthly(t *testing.T) {
	txns, _ := Parse(strings.NewReader(basic))
	b := Balances(txns)
	if b["cash"] != 6950 || b["card"] != -1234 {
		t.Errorf("balances %v", b)
	}
	m := Monthly(txns)
	if len(m) != 2 || m[0] != (MonthSummary{"2026-01", 10000, -3050, 6950}) {
		t.Errorf("monthly %v", m)
	}
}

func TestFormatCents(t *testing.T) {
	for c, want := range map[int64]string{123450: "1,234.50", 0: "0.00", 99: "0.99"} {
		if got := FormatCents(c); got != want {
			t.Errorf("FormatCents(%d) = %q, want %q", c, got, want)
		}
	}
}
