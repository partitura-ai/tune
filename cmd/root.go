package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Gabriel-Feang/tune/filter"
	"github.com/Gabriel-Feang/tune/stats"
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

func cmdFilter(args []string) error {
	// Parse flags
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
			i = len(args) // break
		default:
			cmdArgs = append(cmdArgs, args[i:]...)
			i = len(args) // break
		}
	}

	if len(cmdArgs) == 0 {
		return fmt.Errorf("no command specified")
	}

	// Auto-infer intent from command if not provided
	if intent == "" {
		intent = inferIntent(cmdArgs)
	}

	// Get API key
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
		// On filter failure, fall back to raw output
		fmt.Fprintf(os.Stderr, "[tune] filter error: %v\n", err)
		fmt.Print(rawOutput)
		os.Exit(exitCode)
	}

	// Print filtered output
	fmt.Print(result.Filtered)
	if !strings.HasSuffix(result.Filtered, "\n") {
		fmt.Println()
	}

	// Record stats
	s, _ := stats.Load()
	s.Record(cmdStr, intent, result.RawLen, result.FilterLen, result.FilterTime)
	s.Save() //nolint:errcheck

	// Exit with original exit code
	os.Exit(exitCode)
	return nil // unreachable
}

func runCommand(args []string, passthrough bool) (string, int, error) {
	cmd := exec.Command("sh", "-c", strings.Join(args, " "))

	if passthrough {
		// Show output in real-time AND capture it
		var buf strings.Builder
		cmd.Stdout = io.MultiWriter(os.Stdout, &buf)
		cmd.Stderr = io.MultiWriter(os.Stderr, &buf)
		err := cmd.Run()
		return buf.String(), cmd.ProcessState.ExitCode(), err
	}

	// Capture only
	out, err := cmd.CombinedOutput()
	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	return string(out), exitCode, err
}

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

func printUsage() {
	fmt.Printf(`tune %s — AI-powered command output filter

Usage:
  tune <command>                    Run command, print filtered output
  tune -i "intent" <command>        Run with explicit intent
  tune -p <command>                 Passthrough: show live + filtered summary
  tune gain                         Show token savings stats
  tune gain --history               Show command history with savings

Environment:
  TUNE_API_KEY or OPENROUTER_API_KEY    Required for filtering

Examples:
  tune go test -v ./...
  tune -i "find compilation errors" make build
  tune git diff --stat
  tune gain
`, version)
}
