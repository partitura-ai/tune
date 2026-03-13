package stats

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/partitura-ai/tune/ui"
)

type Entry struct {
	Command    string        `json:"command"`
	Intent     string        `json:"intent"`
	RawTokens  int           `json:"raw_tokens"`
	FilteredTokens int       `json:"filtered_tokens"`
	Savings    float64       `json:"savings_pct"`
	FilterTime time.Duration `json:"filter_time_ms"`
	Timestamp  time.Time     `json:"timestamp"`
}

type Stats struct {
	TotalCommands    int     `json:"total_commands"`
	TotalRawTokens   int     `json:"total_raw_tokens"`
	TotalFilteredTokens int  `json:"total_filtered_tokens"`
	TotalSavings     float64 `json:"total_savings_pct"`
	History          []Entry `json:"history"`
}

func TuneDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tune")
}

func statsPath() string {
	return filepath.Join(TuneDir(), "stats.json")
}

func Load() (*Stats, error) {
	data, err := os.ReadFile(statsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &Stats{}, nil
		}
		return nil, err
	}
	var s Stats
	if err := json.Unmarshal(data, &s); err != nil {
		return &Stats{}, nil
	}
	return &s, nil
}

func (s *Stats) Save() error {
	if err := os.MkdirAll(TuneDir(), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(statsPath(), data, 0644)
}

func (s *Stats) Record(command, intent string, rawChars, filteredChars int, filterTime time.Duration) {
	rawTokens := rawChars / 4
	filteredTokens := filteredChars / 4
	savings := 0.0
	if rawTokens > 0 {
		savings = (1 - float64(filteredTokens)/float64(rawTokens)) * 100
	}

	s.TotalCommands++
	s.TotalRawTokens += rawTokens
	s.TotalFilteredTokens += filteredTokens
	if s.TotalRawTokens > 0 {
		s.TotalSavings = (1 - float64(s.TotalFilteredTokens)/float64(s.TotalRawTokens)) * 100
	}

	s.History = append(s.History, Entry{
		Command:        command,
		Intent:         intent,
		RawTokens:      rawTokens,
		FilteredTokens: filteredTokens,
		Savings:        savings,
		FilterTime:     filterTime,
		Timestamp:      time.Now(),
	})

	// Keep last 100 entries
	if len(s.History) > 100 {
		s.History = s.History[len(s.History)-100:]
	}
}

func (s *Stats) Print() {
	saved := s.TotalRawTokens - s.TotalFilteredTokens

	content := fmt.Sprintf(
		"%s  %s\n%s  %s\n%s  %s\n%s  %s\n%s  %s",
		ui.Label.Render("Commands filtered:"), ui.Value.Render(fmt.Sprintf("%d", s.TotalCommands)),
		ui.Label.Render("Raw tokens:        "), ui.Value.Render(fmt.Sprintf("%d", s.TotalRawTokens)),
		ui.Label.Render("Filtered tokens:   "), ui.Value.Render(fmt.Sprintf("%d", s.TotalFilteredTokens)),
		ui.Label.Render("Tokens saved:      "), ui.Success.Render(fmt.Sprintf("%d", saved)),
		ui.Label.Render("Savings:           "), ui.Success.Bold(true).Render(fmt.Sprintf("%.1f%%", s.TotalSavings)),
	)

	fmt.Println(ui.Title.Render("🎼 Tune — Token Savings"))
	fmt.Println(ui.Box.Render(content))
}

func (s *Stats) PrintHistory() {
	s.Print()
	if len(s.History) == 0 {
		fmt.Println(ui.Faint.Render("\n  No history yet."))
		return
	}

	fmt.Println()
	fmt.Println(ui.Subtitle.Render("Recent commands"))

	start := 0
	if len(s.History) > 20 {
		start = len(s.History) - 20
	}

	rows := make([][]string, 0, len(s.History[start:]))
	for _, e := range s.History[start:] {
		rows = append(rows, []string{
			e.Timestamp.Format("15:04:05"),
			fmt.Sprintf("%d", e.RawTokens),
			fmt.Sprintf("%d", e.FilteredTokens),
			fmt.Sprintf("%.1f%%", e.Savings),
			fmt.Sprintf("%dms", e.FilterTime.Milliseconds()),
			truncateCmd(e.Command, 40),
		})
	}

	t := table.New().
		Headers("Time", "Raw", "Filtered", "Saved", "Latency", "Command").
		Rows(rows...).
		BorderStyle(lipgloss.NewStyle().Foreground(ui.Purple)).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == table.HeaderRow {
				return lipgloss.NewStyle().Bold(true).Foreground(ui.Cyan)
			}
			if col == 3 {
				return lipgloss.NewStyle().Foreground(ui.Green)
			}
			return lipgloss.NewStyle().Foreground(ui.White)
		})

	fmt.Println(t)
}

func truncateCmd(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
