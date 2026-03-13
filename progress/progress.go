package progress

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Durations tracks historical command execution times keyed by (command, directory).
type Durations struct {
	Entries map[string]*DurationEntry `json:"entries"`
}

type DurationEntry struct {
	AvgMs   int64 `json:"avg_ms"`
	Count   int   `json:"count"`
	LastMs  int64 `json:"last_ms"`
}

func durationsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tune", "durations.json")
}

func LoadDurations() *Durations {
	d := &Durations{Entries: map[string]*DurationEntry{}}
	data, err := os.ReadFile(durationsPath())
	if err != nil {
		return d
	}
	if err := json.Unmarshal(data, d); err != nil {
		return &Durations{Entries: map[string]*DurationEntry{}}
	}
	if d.Entries == nil {
		d.Entries = map[string]*DurationEntry{}
	}
	return d
}

func (d *Durations) Save() {
	dir := filepath.Dir(durationsPath())
	os.MkdirAll(dir, 0755)
	data, err := json.Marshal(d)
	if err != nil {
		return
	}
	os.WriteFile(durationsPath(), data, 0644)
}

func durationKey(command, dir string) string {
	return dir + "\x00" + command
}

// Estimate returns the estimated duration for a command in a directory, or 0 if unknown.
func (d *Durations) Estimate(command, dir string) time.Duration {
	e := d.Entries[durationKey(command, dir)]
	if e == nil || e.Count == 0 {
		return 0
	}
	return time.Duration(e.AvgMs) * time.Millisecond
}

// Record stores a new duration observation for a command.
func (d *Durations) Record(command, dir string, elapsed time.Duration) {
	key := durationKey(command, dir)
	e := d.Entries[key]
	if e == nil {
		e = &DurationEntry{}
		d.Entries[key] = e
	}
	ms := elapsed.Milliseconds()
	if e.Count == 0 {
		e.AvgMs = ms
	} else {
		// Exponential moving average (weight recent runs more)
		e.AvgMs = (e.AvgMs*int64(e.Count) + ms) / int64(e.Count+1)
	}
	e.LastMs = ms
	e.Count++
	// Cap count so average stays responsive
	if e.Count > 20 {
		e.Count = 20
	}
}

// Bar displays a progress bar on stderr while a command runs.
// Call Stop() when the command finishes.
type Bar struct {
	command  string
	estimate time.Duration
	start    time.Time
	stop     chan struct{}
	done     sync.WaitGroup
}

// Start begins rendering the progress bar. If estimate is 0 (unknown command),
// shows a spinner without percentage. Only shows for commands estimated >1s.
func Start(command string, estimate time.Duration) *Bar {
	b := &Bar{
		command:  truncate(command, 30),
		estimate: estimate,
		start:    time.Now(),
		stop:     make(chan struct{}),
	}

	// Don't show progress for fast commands
	if estimate > 0 && estimate < 1*time.Second {
		return b
	}

	b.done.Add(1)
	go b.render()
	return b
}

func (b *Bar) Stop() {
	select {
	case <-b.stop:
		return // already stopped
	default:
		close(b.stop)
	}
	b.done.Wait()
	// Clear the progress line
	fmt.Fprintf(os.Stderr, "\r\033[K")
}

func (b *Bar) render() {
	defer b.done.Done()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	spinChars := []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}
	i := 0

	for {
		select {
		case <-b.stop:
			return
		case <-ticker.C:
			elapsed := time.Since(b.start)
			i++
			spin := spinChars[i%len(spinChars)]

			if b.estimate == 0 {
				// Unknown duration — just show spinner + elapsed
				fmt.Fprintf(os.Stderr, "\r\033[K%c %s  %s", spin, b.command, fmtDuration(elapsed))
			} else {
				// Known estimate — show progress bar
				pct := float64(elapsed) / float64(b.estimate)
				if pct > 1.5 {
					pct = 1.5 // cap visual at 150%
				}
				barWidth := 20
				filled := int(pct * float64(barWidth))
				if filled > barWidth {
					filled = barWidth
				}

				bar := ""
				for j := range barWidth {
					if j < filled {
						bar += "█"
					} else {
						bar += "░"
					}
				}

				remaining := b.estimate - elapsed
				if remaining < 0 {
					fmt.Fprintf(os.Stderr, "\r\033[K%c %s  %s [%s] +%s over estimate",
						spin, b.command, fmtDuration(elapsed), bar, fmtDuration(-remaining))
				} else {
					fmt.Fprintf(os.Stderr, "\r\033[K%c %s  %s [%s] ~%s left",
						spin, b.command, fmtDuration(elapsed), bar, fmtDuration(remaining))
				}
			}
		}
	}
}

func fmtDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	s := d.Seconds()
	if s < 60 {
		return fmt.Sprintf("%.1fs", s)
	}
	m := int(s) / 60
	sec := int(s) % 60
	return fmt.Sprintf("%dm%02ds", m, sec)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
