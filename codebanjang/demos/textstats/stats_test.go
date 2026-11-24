package textstats

import "testing"

func TestCount(t *testing.T) {
	cases := []struct {
		in   string
		want Stats
	}{
		{"", Stats{0, 0, 0}},
		{"hello", Stats{1, 1, 5}},
		{"hello world\n", Stats{1, 2, 12}},
		{"a b\nc\n\nd", Stats{4, 4, 8}},
		{"  spaced   out  ", Stats{1, 2, 16}},
		{"안녕 세상", Stats{1, 2, 5}},
		{"tab\tseparated\r\nwindows line", Stats{2, 4, 27}},
	}
	for _, c := range cases {
		if got := Count(c.in); got != c.want {
			t.Errorf("Count(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}
