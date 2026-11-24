// Package textstats counts lines, words and characters in text.
package textstats

// Stats holds the counts for one text.
type Stats struct {
	Lines int // number of lines; a trailing newline does not start a new line
	Words int // runs of non-space characters
	Chars int // Unicode characters (runes), not bytes
}

// Count returns the statistics for text.
func Count(text string) Stats {
	// TODO: not implemented yet
	return Stats{}
}
