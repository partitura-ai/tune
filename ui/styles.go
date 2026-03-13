package ui

import (
	"github.com/charmbracelet/lipgloss"
)

var (
	// Colors
	Purple  = lipgloss.Color("#7C3AED")
	Cyan    = lipgloss.Color("#06B6D4")
	Green   = lipgloss.Color("#10B981")
	Yellow  = lipgloss.Color("#F59E0B")
	Red     = lipgloss.Color("#EF4444")
	Dim     = lipgloss.Color("#6B7280")
	White   = lipgloss.Color("#F9FAFB")

	// Text styles
	Title = lipgloss.NewStyle().
		Bold(true).
		Foreground(Purple)

	Subtitle = lipgloss.NewStyle().
		Bold(true).
		Foreground(Cyan)

	Label = lipgloss.NewStyle().
		Foreground(Dim)

	Value = lipgloss.NewStyle().
		Foreground(White)

	Success = lipgloss.NewStyle().
		Foreground(Green)

	Warning = lipgloss.NewStyle().
		Foreground(Yellow)

	Error = lipgloss.NewStyle().
		Foreground(Red)

	Accent = lipgloss.NewStyle().
		Foreground(Purple).
		Bold(true)

	Faint = lipgloss.NewStyle().
		Foreground(Dim)

	// Box for sections
	Box = lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(Purple).
		Padding(0, 1)

	// Slogan style
	Slogan = lipgloss.NewStyle().
		Italic(true).
		Foreground(Dim)
)
