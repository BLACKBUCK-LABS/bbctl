package commands

import "testing"

func TestUnescapeLocalPath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`/Users/k/Downloads/11th\ AGM\ Notice\ 2025-26.pdf`, `/Users/k/Downloads/11th AGM Notice 2025-26.pdf`},
		{`"/Users/k/Downloads/11th AGM Notice 2025-26.pdf"`, `/Users/k/Downloads/11th AGM Notice 2025-26.pdf`},
		{`'/Users/k/Downloads/report.csv'`, `/Users/k/Downloads/report.csv`},
		{`/Users/k/Downloads/report.csv`, `/Users/k/Downloads/report.csv`},
		{`-`, `-`},
	}
	for _, c := range cases {
		if got := unescapeLocalPath(c.in); got != c.want {
			t.Errorf("unescapeLocalPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
