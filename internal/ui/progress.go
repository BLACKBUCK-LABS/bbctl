package ui

import (
	"fmt"
	"io"
	"strings"
)

// HumanBytes formats a byte count as B/KB/MB/GB.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

// RenderBar renders a determinate progress bar string.
func RenderBar(done, total int64, width int) string {
	if total <= 0 {
		total = 1
	}
	if done > total {
		done = total
	}
	ratio := float64(done) / float64(total)
	filledN := int(ratio * float64(width))
	fillCh, emptyCh := "█", "░"
	if !Std.Unicode {
		fillCh, emptyCh = "#", "-"
	}
	bar := Render(Brand, strings.Repeat(fillCh, filledN)) +
		Render(Dim, strings.Repeat(emptyCh, width-filledN))
	return fmt.Sprintf("%s %3.0f%%  %s/%s",
		bar, ratio*100, HumanBytes(done), HumanBytes(total))
}

// CountingReader wraps an io.Reader and redraws a bar as bytes flow.
type CountingReader struct {
	r     io.Reader
	w     io.Writer
	total int64
	done  int64
}

// NewCountingReader wraps r, drawing progress to w (skipped on non-TTY).
func NewCountingReader(r io.Reader, total int64, w io.Writer) *CountingReader {
	return &CountingReader{r: r, w: w, total: total}
}

func (c *CountingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.done += int64(n)
	if Std.TTY {
		fmt.Fprintf(c.w, "\r%s", RenderBar(c.done, c.total, 24))
	}
	return n, err
}

// Finish clears the bar line (TTY only).
func (c *CountingReader) Finish() {
	if Std.TTY {
		fmt.Fprintf(c.w, "\r\033[K")
	}
}
