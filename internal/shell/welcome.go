package shell

import (
	"fmt"
	"strings"
)

const (
	colorReset  = "\033[0m"
	colorOrange = "\033[38;5;208m"
	colorCyan   = "\033[38;5;51m"
	colorWhite  = "\033[97m"
	colorGray   = "\033[38;5;245m"
	colorBold   = "\033[1m"
)

var bbctlASCII = []string{
	` ██████╗ ██████╗  ██████╗████████╗██╗`,
	` ██╔══██╗██╔══██╗██╔════╝╚══██╔══╝██║`,
	` ██████╔╝██████╔╝██║        ██║   ██║`,
	` ██╔══██╗██╔══██╗██║        ██║   ██║`,
	` ██████╔╝██████╔╝╚██████╗   ██║   ███████╗`,
	` ╚═════╝ ╚═════╝  ╚═════╝   ╚═╝   ╚══════╝`,
}

type WelcomeInfo struct {
	Email         string
	Version       string
	InstanceCount int
	CacheAge      string
	AccountCount  int
}

func PrintWelcome(info WelcomeInfo) {
	width := 60

	border  := colorOrange + "║" + colorReset
	topBar  := colorOrange + "╔" + strings.Repeat("═", width-2) + "╗" + colorReset
	botBar  := colorOrange + "╚" + strings.Repeat("═", width-2) + "╝" + colorReset
	divider := colorOrange + "║" + colorGray + strings.Repeat("─", width-2) + colorOrange + "║" + colorReset

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
			color+content+colorReset,
			strings.Repeat(" ", right), border, colorReset)
	}

	leftLine := func(content, color string) {
		visible := visibleLen(content)
		padding := width - 2 - visible - 2
		if padding < 0 {
			padding = 0
		}
		fmt.Printf("%s  %s%s%s%s%s\n",
			border, color, content, colorReset,
			strings.Repeat(" ", padding), border)
	}

	fmt.Println(topBar)
	emptyLine()
	for _, line := range bbctlASCII {
		padLine(line, colorOrange+colorBold)
	}
	emptyLine()
	padLine("Gated EC2 Access  —  Blackbuck Engineering", colorGray)
	emptyLine()
	fmt.Println(divider)
	emptyLine()

	leftLine(fmt.Sprintf("%-14s %s%s%s", "Logged in as:", colorCyan, info.Email, colorReset), colorGray)
	leftLine(fmt.Sprintf("%-14s %s%s%s", "Version:", colorWhite, info.Version, colorReset), colorGray)

	if info.InstanceCount > 0 {
		cacheStr := fmt.Sprintf("%d instances across %d accounts", info.InstanceCount, info.AccountCount)
		if info.CacheAge != "" {
			cacheStr += colorGray + "  (" + info.CacheAge + ")" + colorReset
		}
		leftLine(fmt.Sprintf("%-14s %s%s%s", "Instances:", colorWhite, cacheStr, colorReset), colorGray)
	} else {
		leftLine(fmt.Sprintf("%-14s %s%s", "Instances:", colorGray, "loading..."), colorGray)
	}

	leftLine("💬 Issues? Reach out to Krishna (#infra-devops)", colorGray)

	emptyLine()
	fmt.Println(botBar)
	fmt.Println()
}

// visibleLen returns the length of a string without ANSI escape codes.
func visibleLen(s string) int {
	inEsc := false
	count := 0
	for _, c := range s {
		if c == '\033' {
			inEsc = true
			continue
		}
		if inEsc {
			if c == 'm' {
				inEsc = false
			}
			continue
		}
		count++
	}
	return count
}
