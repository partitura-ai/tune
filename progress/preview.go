package progress

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"golang.org/x/term"
)

// Preview shows the last N lines of raw output on stderr as ephemeral text.
// Lines are dimmed and overwritten in place — they disappear when Clear is called.
type Preview struct {
	mu       sync.Mutex
	lines    []string
	maxLines int
	drawn    int // how many lines are currently rendered on screen
}

// NewPreview creates a preview that shows the last n lines.
func NewPreview(n int) *Preview {
	return &Preview{
		maxLines: n,
	}
}

// AddLine adds a new line to the preview and re-renders.
func (p *Preview) AddLine(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.lines = append(p.lines, line)
	if len(p.lines) > p.maxLines {
		p.lines = p.lines[len(p.lines)-p.maxLines:]
	}

	p.render()
}

// Clear removes the preview from the terminal.
func (p *Preview) Clear() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.erase()
	p.lines = nil
	p.drawn = 0
}

// erase removes currently drawn preview lines from the terminal.
func (p *Preview) erase() {
	if p.drawn == 0 {
		return
	}
	// Move up and clear each line we previously drew
	for range p.drawn {
		fmt.Fprintf(os.Stderr, "\033[A\033[K")
	}
	p.drawn = 0
}

// render draws the current preview lines on stderr.
func (p *Preview) render() {
	p.erase()

	if len(p.lines) == 0 {
		return
	}

	width := termWidth()

	for _, line := range p.lines {
		display := truncateLine(line, width)
		// Dim color (ANSI dim + dark gray)
		fmt.Fprintf(os.Stderr, "\033[2;90m%s\033[0m\n", display)
	}
	p.drawn = len(p.lines)
}

// termWidth returns the terminal width, defaulting to 80.
func termWidth() int {
	w, _, err := term.GetSize(int(os.Stderr.Fd()))
	if err != nil || w <= 0 {
		return 80
	}
	return w
}

// truncateLine shortens a line to fit the terminal width.
func truncateLine(s string, maxWidth int) string {
	// Strip any existing ANSI codes from raw output
	s = strings.TrimRight(s, "\r\n")
	if len(s) > maxWidth-2 {
		return s[:maxWidth-5] + "..."
	}
	return s
}
