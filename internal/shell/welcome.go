package shell

import (
	"fmt"
	"strings"

	"github.com/blackbuck/bbctl/internal/ui"
)

const (
	colorReset = "\033[0m"
	colorRed   = "\033[38;5;167m"
	colorCyan  = "\033[38;5;51m"
	colorWhite = "\033[97m"
	colorGray  = "\033[38;5;245m"
	colorBold  = "\033[1m"
)

var bbctlASCII = []string{

	` ▄▄▄▄▄▄    ▄▄▄▄▄▄       ▄▄▄▄   ▄▄▄▄▄▄▄▄  ▄▄        `,
	` ██▀▀▀▀██  ██▀▀▀▀██   ██▀▀▀▀█  ▀▀▀██▀▀▀  ██       `,
	` ██    ██  ██    ██  ██▀          ██     ██       `,
	` ███████   ███████   ██           ██     ██       `,
	` ██    ██  ██    ██  ██▄          ██     ██       `,
	` ██▄▄▄▄██  ██▄▄▄▄██   ██▄▄▄▄█     ██     ██▄▄▄▄▄▄ `,
	` ▀▀▀▀▀▀▀   ▀▀▀▀▀▀▀      ▀▀▀▀      ▀▀     ▀▀▀▀▀▀▀▀ `,
}

type WelcomeInfo struct {
	Email         string
	Version       string
	InstanceCount int
	CacheAge      string
	AccountCount  int
}

func PrintWelcome(info WelcomeInfo) {
	if !ui.Std.TTY {
		return
	}
	// Local shadows: stripped to "" when color is unsupported so the panel
	// emits zero ANSI escapes on piped/NO_COLOR output. Package-level consts
	// remain intact for visibleLen() which needs them to strip sequences.
	cReset, cRed, cCyan := colorReset, colorRed, colorCyan
	cWhite, cGray, cBold := colorWhite, colorGray, colorBold
	if !ui.Std.Color {
		cReset, cRed, cCyan = "", "", ""
		cWhite, cGray, cBold = "", "", ""
	}

	width := 55

	border := cRed + "║" + cReset
	topBar := cRed + "╔" + strings.Repeat("═", width-2) + "╗" + cReset
	botBar := cRed + "╚" + strings.Repeat("═", width-2) + "╝" + cReset
	divider := cRed + "║" + cGray + strings.Repeat("─", width-2) + cRed + "║" + cReset

	emptyLine := func() {
		fmt.Printf("%s%s%s\n", border, strings.Repeat(" ", width-2), border)
	}

	padLine := func(content, color string) {
		visible := visibleLen(content)
		padding := width - 2 - visible
		if padding < 0 {
			padding = 0
		}
		left := padding / 2
		right := padding - left
		fmt.Printf("%s%s%s%s%s%s\n",
			border, strings.Repeat(" ", left),
			color+content+cReset,
			strings.Repeat(" ", right), border, cReset)
	}

	leftLine := func(content, color string) {
		visible := visibleLen(content)
		padding := width - 2 - visible - 2
		if padding < 0 {
			padding = 0
		}
		fmt.Printf("%s  %s%s%s%s%s\n",
			border, color, content, cReset,
			strings.Repeat(" ", padding), border)
	}

	fmt.Println(topBar)
	emptyLine()
	for _, line := range bbctlASCII {
		padLine(line, cRed+cBold)
	}
	emptyLine()
	padLine("Gated EC2 Access  —  Blackbuck Engineering", cGray)
	emptyLine()
	fmt.Println(divider)
	emptyLine()

	leftLine(fmt.Sprintf("%-14s %s%s%s", "Logged in as:", cCyan, info.Email, cReset), cGray)
	leftLine(fmt.Sprintf("%-14s %s%s%s", "Version:", cWhite, info.Version, cReset), cGray)

	if info.InstanceCount > 0 {
		cacheStr := fmt.Sprintf("%d across %d accounts (%s)", info.InstanceCount, info.AccountCount, info.CacheAge)
		leftLine(fmt.Sprintf("%-14s %s%s%s", "Instances:", cWhite, cacheStr, cReset), cGray)
	} else {
		leftLine(fmt.Sprintf("%-14s %s%s", "Instances:", cGray, "loading..."), cGray)
	}

	leftLine("Issues? Reach out to Krishna (#infra-devops)", cGray)

	emptyLine()
	fmt.Println(botBar)
	fmt.Println()
}

// visibleLen returns the number of terminal columns a string occupies,
// stripping ANSI escape sequences. Standard ASCII only — one rune = one column.
func visibleLen(s string) int {
	inEsc := false
	count := 0
	for _, r := range s {
		if r == '\033' {
			inEsc = true
			continue
		}
		if inEsc {
			if r == 'm' {
				inEsc = false
			}
			continue
		}
		count++
	}
	return count
}
