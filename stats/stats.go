package stats

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
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
	fmt.Printf("Tune — Token Savings\n")
	fmt.Printf("════════════════════════════════════\n")
	fmt.Printf("  Commands filtered:  %d\n", s.TotalCommands)
	fmt.Printf("  Raw tokens:         %d\n", s.TotalRawTokens)
	fmt.Printf("  Filtered tokens:    %d\n", s.TotalFilteredTokens)
	fmt.Printf("  Tokens saved:       %d\n", s.TotalRawTokens-s.TotalFilteredTokens)
	fmt.Printf("  Savings:            %.1f%%\n", s.TotalSavings)
	fmt.Printf("════════════════════════════════════\n")
}

func (s *Stats) PrintHistory() {
	s.Print()
	if len(s.History) == 0 {
		fmt.Println("\n  No history yet.")
		return
	}
	fmt.Printf("\nRecent commands:\n")
	start := 0
	if len(s.History) > 20 {
		start = len(s.History) - 20
	}
	for _, e := range s.History[start:] {
		fmt.Printf("  %s  %5d → %3d tokens (%5.1f%%)  %4dms  %s\n",
			e.Timestamp.Format("15:04:05"),
			e.RawTokens, e.FilteredTokens, e.Savings,
			e.FilterTime.Milliseconds(),
			truncateCmd(e.Command, 40),
		)
	}
}

func truncateCmd(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
