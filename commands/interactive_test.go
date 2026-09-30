package commands

import "testing"

func TestStripSurroundingQuotes(t *testing.T) {
	cases := map[string]string{
		`"C:\Users\me\file.txt"`: `C:\Users\me\file.txt`, // Explorer "Copy as path"
		`'C:\Users\me\file.txt'`: `C:\Users\me\file.txt`,
		`C:\Users\me\file.txt`:   `C:\Users\me\file.txt`, // no quotes, unchanged
		`"unbalanced`:            `"unbalanced`,          // only one quote, unchanged
		`"`:                      `"`,                    // single char, unchanged
		``:                       ``,
	}
	for in, want := range cases {
		if got := stripSurroundingQuotes(in); got != want {
			t.Errorf("stripSurroundingQuotes(%q) = %q, want %q", in, got, want)
		}
	}
}
