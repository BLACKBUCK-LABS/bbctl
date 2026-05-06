package shell

import (
	"fmt"
	"strings"
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
	width := 55

	border  := colorRed + "║" + colorReset
	topBar  := colorRed + "╔" + strings.Repeat("═", width-2) + "╗" + colorReset
	botBar  := colorRed + "╚" + strings.Repeat("═", width-2) + "╝" + colorReset
	divider := colorRed + "║" + colorGray + strings.Repeat("─", width-2) + colorRed + "║" + colorReset

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
		padLine(line, colorRed+colorBold)
	}
	emptyLine()
	padLine("Gated EC2 Access  —  Blackbuck Engineering", colorGray)
	emptyLine()
	fmt.Println(divider)
	emptyLine()

	leftLine(fmt.Sprintf("%-14s %s%s%s", "Logged in as:", colorCyan, info.Email, colorReset), colorGray)
	leftLine(fmt.Sprintf("%-14s %s%s%s", "Version:", colorWhite, info.Version, colorReset), colorGray)

	if info.InstanceCount > 0 {
		cacheStr := fmt.Sprintf("%d across %d accounts (%s)", info.InstanceCount, info.AccountCount, info.CacheAge)
		leftLine(fmt.Sprintf("%-14s %s%s%s", "Instances:", colorWhite, cacheStr, colorReset), colorGray)
	} else {
		leftLine(fmt.Sprintf("%-14s %s%s", "Instances:", colorGray, "loading..."), colorGray)
	}

	leftLine("Issues? Reach out to Krishna (#infra-devops)", colorGray)

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
