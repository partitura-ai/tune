package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Gabriel-Feang/tune/filter"
	"github.com/Gabriel-Feang/tune/stats"
	"github.com/Gabriel-Feang/tune/registry"
)

const version = "0.1.0"

func Execute() error {
	args := os.Args[1:]

	if len(args) == 0 {
		printUsage()
		return nil
	}

	switch args[0] {
	case "gain":
		return cmdGain(args[1:])
	case "add":
		return cmdAdd(args[1:])
	case "remove", "rm":
		return cmdRemove(args[1:])
	case "list", "ls":
		return cmdList()
	case "init":
		return cmdInit(args[1:])
	case "version", "--version", "-v":
		fmt.Printf("tune %s\n", version)
		return nil
	case "help", "--help", "-h":
		printUsage()
		return nil
	default:
		return cmdFilter(args)
	}
}

// ---------------------------------------------------------------------------
// tune gain
// ---------------------------------------------------------------------------

func cmdGain(args []string) error {
	s, err := stats.Load()
	if err != nil {
		return err
	}
	showHistory := false
	for _, a := range args {
		if a == "--history" || a == "-h" {
			showHistory = true
		}
	}
	if showHistory {
		s.PrintHistory()
	} else {
		s.Print()
	}
	return nil
}

// ---------------------------------------------------------------------------
// tune add / remove / list — command registration
// ---------------------------------------------------------------------------

func cmdAdd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: tune add <command> [command...]")
	}
	reg, err := registry.Load()
	if err != nil {
		return err
	}
	for _, cmd := range args {
		reg.Add(cmd)
		fmt.Printf("  registered: %s\n", cmd)
	}
	if err := reg.Save(); err != nil {
		return err
	}
	fmt.Printf("\nRun 'eval \"$(tune init)\"' or add it to your shell profile to activate.\n")
	return nil
}

func cmdRemove(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: tune remove <command> [command...]")
	}
	reg, err := registry.Load()
	if err != nil {
		return err
	}
	for _, cmd := range args {
		reg.Remove(cmd)
		fmt.Printf("  unregistered: %s\n", cmd)
	}
	if err := reg.Save(); err != nil {
		return err
	}
	fmt.Printf("\nRestart your shell or run 'eval \"$(tune init)\"' to apply.\n")
	return nil
}

func cmdList() error {
	reg, err := registry.Load()
	if err != nil {
		return err
	}
	cmds := reg.List()
	if len(cmds) == 0 {
		fmt.Println("No commands registered. Use 'tune add <command>' to register one.")
		return nil
	}
	fmt.Println("Registered commands:")
	for _, cmd := range cmds {
		fmt.Printf("  %s\n", cmd)
	}
	return nil
}

// ---------------------------------------------------------------------------
// tune init — outputs shell functions for eval
// ---------------------------------------------------------------------------

func cmdInit(args []string) error {
	shell := "zsh"
	for _, a := range args {
		if a == "--bash" {
			shell = "bash"
		}
	}
	_ = shell // both use the same syntax

	tuneBin, _ := os.Executable()
	if tuneBin == "" {
		tuneBin = "tune"
	}

	reg, err := registry.Load()
	if err != nil {
		return err
	}

	cmds := reg.List()
	if len(cmds) == 0 {
		fmt.Fprintln(os.Stderr, "# tune: no commands registered, nothing to init")
		return nil
	}

	// Emit a shell function for each registered command.
	// The function calls `tune` with the original command, passing all args.
	// `command <cmd>` bypasses the function to call the real binary.
	for _, cmd := range cmds {
		fmt.Printf(`# tune wrapper for: %s
%s() {
  %s command %s "$@"
}
`, cmd, cmd, tuneBin, cmd)
	}

	return nil
}

// ---------------------------------------------------------------------------
// tune <command> — filter
// ---------------------------------------------------------------------------

func cmdFilter(args []string) error {
	intent := ""
	passthrough := false
	var cmdArgs []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--intent", "-i":
			if i+1 < len(args) {
				i++
				intent = args[i]
			}
		case "--passthrough", "-p":
			passthrough = true
		case "--":
			cmdArgs = append(cmdArgs, args[i+1:]...)
			i = len(args)
		default:
			cmdArgs = append(cmdArgs, args[i:]...)
			i = len(args)
		}
	}

	if len(cmdArgs) == 0 {
		return fmt.Errorf("no command specified")
	}

	if intent == "" {
		intent = inferIntent(cmdArgs)
	}

	apiKey := os.Getenv("TUNE_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("OPENROUTER_API_KEY")
	}
	if apiKey == "" {
		return fmt.Errorf("no API key: set TUNE_API_KEY or OPENROUTER_API_KEY")
	}

	// Run command
	cmdStr := strings.Join(cmdArgs, " ")
	rawOutput, exitCode, err := runCommand(cmdArgs, passthrough)
	if err != nil && rawOutput == "" {
		return fmt.Errorf("command failed to start: %w", err)
	}

	// Always write full output to a tee file so the agent can check it
	teeFile := writeTeeFile(cmdStr, rawOutput)

	// Skip filtering for tiny output (not worth the API call)
	rawTokens := len(rawOutput) / 4
	if rawTokens < 20 {
		fmt.Print(rawOutput)
		os.Exit(exitCode)
	}

	// Filter
	cfg := filter.DefaultConfig()
	cfg.APIKey = apiKey

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result, err := filter.Filter(ctx, cfg, rawOutput, exitCode, intent)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[tune] filter error: %v\n", err)
		fmt.Print(rawOutput)
		os.Exit(exitCode)
	}

	// Print filtered output with tee file reference
	fmt.Print(result.Filtered)
	if !strings.HasSuffix(result.Filtered, "\n") {
		fmt.Println()
	}
	if teeFile != "" {
		fmt.Fprintf(os.Stderr, "[full output: %s]\n", teeFile)
	}

	// Record stats
	s, _ := stats.Load()
	s.Record(cmdStr, intent, result.RawLen, result.FilterLen, result.FilterTime)
	s.Save() //nolint:errcheck

	os.Exit(exitCode)
	return nil
}

// ---------------------------------------------------------------------------
// Tee file — write full raw output for later inspection
// ---------------------------------------------------------------------------

func writeTeeFile(cmdStr, rawOutput string) string {
	tuneDir := stats.TuneDir()
	teeDir := filepath.Join(tuneDir, "tee")
	if err := os.MkdirAll(teeDir, 0755); err != nil {
		return ""
	}

	// Use timestamp + sanitized command name
	ts := time.Now().UnixMilli()
	cmdName := sanitizeFilename(cmdStr)
	if len(cmdName) > 40 {
		cmdName = cmdName[:40]
	}
	filename := fmt.Sprintf("%d_%s.log", ts, cmdName)
	path := filepath.Join(teeDir, filename)

	if err := os.WriteFile(path, []byte(rawOutput), 0644); err != nil {
		return ""
	}

	// Clean up old tee files (keep last 50)
	cleanTeeDir(teeDir, 50)

	return path
}

func sanitizeFilename(s string) string {
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, ".", "_")
	s = strings.ReplaceAll(s, "-", "_")
	var clean strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			clean.WriteRune(r)
		}
	}
	return clean.String()
}

func cleanTeeDir(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= keep {
		return
	}
	// Entries are sorted by name (timestamp prefix), so oldest first
	toRemove := len(entries) - keep
	for i := 0; i < toRemove; i++ {
		os.Remove(filepath.Join(dir, entries[i].Name()))
	}
}

// ---------------------------------------------------------------------------
// Command execution
// ---------------------------------------------------------------------------

func runCommand(args []string, passthrough bool) (string, int, error) {
	cmd := exec.Command("sh", "-c", strings.Join(args, " "))

	if passthrough {
		var buf strings.Builder
		cmd.Stdout = io.MultiWriter(os.Stdout, &buf)
		cmd.Stderr = io.MultiWriter(os.Stderr, &buf)
		err := cmd.Run()
		exitCode := 0
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
		return buf.String(), exitCode, err
	}

	out, err := cmd.CombinedOutput()
	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	return string(out), exitCode, err
}

// ---------------------------------------------------------------------------
// Intent inference
// ---------------------------------------------------------------------------

func inferIntent(args []string) string {
	cmd := strings.Join(args, " ")
	switch {
	case strings.Contains(cmd, "test"):
		return "check for test failures"
	case strings.Contains(cmd, "build") || strings.Contains(cmd, "compile"):
		return "check for build errors"
	case strings.Contains(cmd, "lint") || strings.Contains(cmd, "vet"):
		return "check for lint errors"
	case strings.Contains(cmd, "git diff"):
		return "summarize changes"
	case strings.Contains(cmd, "git status"):
		return "what files changed"
	case strings.Contains(cmd, "git log"):
		return "summarize recent commits"
	case strings.Contains(cmd, "npm") || strings.Contains(cmd, "yarn") || strings.Contains(cmd, "pnpm"):
		return "check for errors"
	default:
		return "extract essential information"
	}
}

// ---------------------------------------------------------------------------
// Usage
// ---------------------------------------------------------------------------

func printUsage() {
	fmt.Printf(`tune %s — AI-powered command output filter

Usage:
  tune <command>                    Run command, print filtered output
  tune -i "intent" <command>        Run with explicit intent
  tune -p <command>                 Passthrough: show live + filtered summary
  tune gain                         Show token savings stats
  tune gain --history               Show command history with savings

Command registration:
  tune add <command>                Register a command for interception
  tune remove <command>             Unregister a command
  tune list                         Show registered commands
  tune init                         Output shell functions (eval this)

Setup:
  tune add go git npm               Register commands
  eval "$(tune init)"               Activate in current shell
  # Or add to ~/.zshrc:
  echo 'eval "$(tune init)"' >> ~/.zshrc

Environment:
  TUNE_API_KEY or OPENROUTER_API_KEY    Required for filtering

Examples:
  tune go test -v ./...
  tune -i "find compilation errors" make build
  tune git diff --stat
  tune gain
`, version)
}
