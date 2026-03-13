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

	"github.com/partitura-ai/tune/config"
	"github.com/partitura-ai/tune/filter"
	"github.com/partitura-ai/tune/progress"
	"github.com/partitura-ai/tune/registry"
	"github.com/partitura-ai/tune/stats"
)

const version = "1.0.0"

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
	case "config":
		return cmdConfig(args[1:])
	case "image", "img":
		return cmdImage(args[1:])
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
// tune config — configuration management
// ---------------------------------------------------------------------------

func cmdConfig(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if len(args) == 0 {
		cfg.Print()
		return nil
	}

	switch args[0] {
	case "provider":
		if len(args) < 2 {
			fmt.Printf("Current provider: %s\n", cfg.Provider)
			fmt.Printf("Available: %s\n", strings.Join(config.Providers(), ", "))
			return nil
		}
		p := args[1]
		if !config.ValidProvider(p) {
			return fmt.Errorf("unknown provider %q — available: %s", p, strings.Join(config.Providers(), ", "))
		}
		cfg.Provider = p
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("Provider set to: %s\n", p)

	case "model":
		if len(args) < 2 {
			fmt.Printf("Current model: %s\n", cfg.Model)
			return nil
		}
		cfg.Model = args[1]
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("Model set to: %s\n", args[1])

	case "apikey":
		if len(args) < 2 {
			fmt.Printf("Set API key for current provider (%s):\n", cfg.Provider)
			fmt.Printf("  tune config apikey <key>\n")
			fmt.Printf("  tune config apikey <provider> <key>\n")
			return nil
		}
		provider := cfg.Provider
		key := args[1]
		if len(args) >= 3 {
			provider = args[1]
			key = args[2]
			if !config.ValidProvider(provider) {
				return fmt.Errorf("unknown provider %q", provider)
			}
		}
		cfg.APIKeys[provider] = key
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("API key set for: %s\n", provider)

	case "or-provider":
		if len(args) < 2 {
			fmt.Printf("Current OpenRouter provider: %s\n", cfg.OpenRouterProvider)
			return nil
		}
		cfg.OpenRouterProvider = args[1]
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("OpenRouter provider set to: %s\n", args[1])

	case "max-input":
		if len(args) < 2 {
			fmt.Printf("Current max input: %d chars\n", cfg.MaxInput)
			return nil
		}
		var n int
		if _, err := fmt.Sscanf(args[1], "%d", &n); err != nil || n < 100 {
			return fmt.Errorf("invalid max-input: must be a number >= 100")
		}
		cfg.MaxInput = n
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("Max input set to: %d chars\n", n)

	case "stream":
		if len(args) < 2 {
			fmt.Printf("Stream mode: %v\n", cfg.Stream)
			return nil
		}
		switch args[1] {
		case "on", "true", "yes":
			cfg.Stream = true
		case "off", "false", "no":
			cfg.Stream = false
		default:
			return fmt.Errorf("invalid value %q — use: on/off", args[1])
		}
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("Stream mode: %v\n", cfg.Stream)

	default:
		return fmt.Errorf("unknown config key %q — use: provider, model, apikey, or-provider, max-input, stream", args[0])
	}
	return nil
}

// ---------------------------------------------------------------------------
// tune image — filter image with resolution backoff
// ---------------------------------------------------------------------------

func cmdImage(args []string) error {
	intent := ""
	var imagePath string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--intent", "-i":
			if i+1 < len(args) {
				i++
				intent = args[i]
			}
		default:
			imagePath = args[i]
		}
	}

	if imagePath == "" {
		return fmt.Errorf("usage: tune image [-i intent] <image-path>")
	}

	imageData, err := os.ReadFile(imagePath)
	if err != nil {
		return fmt.Errorf("failed to read image: %w", err)
	}

	mimeType := filter.DetectMIME(imageData)
	if !strings.HasPrefix(mimeType, "image/") {
		return fmt.Errorf("file does not appear to be an image: %s", mimeType)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := filter.FilterImage(ctx, cfg, imageData, mimeType, intent)
	if err != nil {
		return fmt.Errorf("image filter error: %w", err)
	}

	fmt.Print(result.Filtered)
	if !strings.HasSuffix(result.Filtered, "\n") {
		fmt.Println()
	}

	// Record stats
	s, _ := stats.Load()
	s.Record("image:"+filepath.Base(imagePath), intent, result.RawLen, result.FilterLen, result.FilterTime)
	s.Save() //nolint:errcheck

	return nil
}

// ---------------------------------------------------------------------------
// tune <command> — filter
// ---------------------------------------------------------------------------

func cmdFilter(args []string) error {
	intent := ""
	passthrough := false
	streamOverride := 0 // 0=use config, 1=force on, -1=force off
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
		case "--stream", "-s":
			streamOverride = 1
		case "--no-stream":
			streamOverride = -1
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

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if cfg.ActiveAPIKey() == "" && cfg.Provider != "ollama" {
		return fmt.Errorf("no API key: set via 'tune config apikey <key>' or TUNE_API_KEY env var")
	}

	cmdStr := strings.Join(cmdArgs, " ")
	cwd, _ := os.Getwd()

	// Resolve streaming: flag overrides config
	streaming := cfg.Stream
	if streamOverride == 1 {
		streaming = true
	} else if streamOverride == -1 {
		streaming = false
	}

	// Streaming mode: filter output in chunks as the command runs
	if streaming && !passthrough {
		return cmdFilterStreaming(cfg, cmdArgs, cmdStr, cwd, intent)
	}

	// Standard mode: run command, then filter
	durations := progress.LoadDurations()
	estimate := durations.Estimate(cmdStr, cwd)
	bar := progress.Start(cmdStr, estimate)

	cmdStart := time.Now()
	rawOutput, exitCode, runErr := runCommand(cmdArgs, passthrough)
	cmdElapsed := time.Since(cmdStart)
	bar.Stop()

	durations.Record(cmdStr, cwd, cmdElapsed)
	durations.Save()

	if runErr != nil && rawOutput == "" {
		return fmt.Errorf("command failed to start: %w", runErr)
	}

	teeFile := writeTeeFile(cmdStr, rawOutput)

	rawTokens := len(rawOutput) / 4
	if rawTokens < 20 {
		fmt.Print(rawOutput)
		os.Exit(exitCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var result *filter.Result
	var filterErr error

	if exitCode != 0 {
		result, filterErr = filter.FilterStream(ctx, cfg, os.Stdout, rawOutput, exitCode, intent)
		if filterErr == nil && !strings.HasSuffix(result.Filtered, "\n") {
			fmt.Println()
		}
	} else {
		result, filterErr = filter.Filter(ctx, cfg, rawOutput, exitCode, intent)
		if filterErr == nil {
			fmt.Print(result.Filtered)
			if !strings.HasSuffix(result.Filtered, "\n") {
				fmt.Println()
			}
		}
	}

	if filterErr != nil {
		fmt.Fprintf(os.Stderr, "[tune] filter error: %v\n", filterErr)
		fmt.Print(rawOutput)
		os.Exit(exitCode)
	}

	if teeFile != "" {
		fmt.Fprintf(os.Stderr, "[full output: %s]\n", teeFile)
	}

	s, _ := stats.Load()
	s.Record(cmdStr, intent, result.RawLen, result.FilterLen, result.FilterTime)
	s.Save() //nolint:errcheck

	os.Exit(exitCode)
	return nil
}

// cmdFilterStreaming runs a command and filters its output in real-time chunks.
func cmdFilterStreaming(cfg *config.Config, cmdArgs []string, cmdStr, cwd, intent string) error {
	_ = intent // TODO: pass intent into chunk prompts

	cmd := exec.Command("sh", "-c", strings.Join(cmdArgs, " "))

	// Merge stdout and stderr into a single pipe
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	// Also tee raw output for the tee file
	var rawBuf strings.Builder
	teeReader := io.TeeReader(pr, &rawBuf)

	exitCodeCh := make(chan int, 1)

	cmdStart := time.Now()
	if err := cmd.Start(); err != nil {
		pw.Close()
		return fmt.Errorf("command failed to start: %w", err)
	}

	// When the command exits, close the write end and send exit code
	go func() {
		cmd.Wait()
		pw.Close()
		exitCode := 0
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
		exitCodeCh <- exitCode
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cc := filter.DefaultChunkedConfig()
	result, filterErr := filter.FilterChunked(ctx, cfg, cc, teeReader, os.Stdout, exitCodeCh)

	cmdElapsed := time.Since(cmdStart)

	// Record duration
	durations := progress.LoadDurations()
	durations.Record(cmdStr, cwd, cmdElapsed)
	durations.Save()

	// Write tee file with full raw output
	teeFile := writeTeeFile(cmdStr, rawBuf.String())

	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	if filterErr != nil {
		fmt.Fprintf(os.Stderr, "[tune] chunked filter error: %v\n", filterErr)
	}

	if teeFile != "" {
		fmt.Fprintf(os.Stderr, "[full output: %s]\n", teeFile)
	}

	if result != nil {
		fmt.Fprintf(os.Stderr, "[tune] %d chunks, %d→%d chars, %dms filter time\n",
			result.ChunkCount, result.RawLen, result.FilterLen, result.FilterTime.Milliseconds())

		s, _ := stats.Load()
		s.Record(cmdStr, "streaming", result.RawLen, result.FilterLen, result.FilterTime)
		s.Save() //nolint:errcheck
	}

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
  tune -s <command>                 Stream: filter output in real-time chunks
  tune image <image-path>           Filter/extract info from image
  tune image -i "intent" <path>     Image with explicit intent
  tune gain                         Show token savings stats
  tune gain --history               Show command history with savings

Configuration:
  tune config                       Show current config
  tune config provider <name>       Set provider (openrouter, openai, anthropic, gemini, ollama)
  tune config model <model>         Set model
  tune config apikey <key>          Set API key for current provider
  tune config apikey <prov> <key>   Set API key for specific provider
  tune config or-provider <name>    Set OpenRouter sub-provider (e.g. groq)
  tune config max-input <chars>     Set max input chars

Command registration:
  tune add <command>                Register a command for interception
  tune remove <command>             Unregister a command
  tune list                         Show registered commands
  tune init                         Output shell functions (eval this)

Setup:
  tune add go git npm               Register commands
  eval "$(tune init)"               Activate in current shell

Environment (fallbacks if no config file):
  TUNE_API_KEY                      API key for active provider
  OPENROUTER_API_KEY                OpenRouter API key
  OPENAI_API_KEY                    OpenAI API key
  ANTHROPIC_API_KEY                 Anthropic API key
  GEMINI_API_KEY                    Gemini API key

Examples:
  tune go test -v ./...
  tune -i "find compilation errors" make build
  tune image screenshot.png
  tune config provider openai
  tune config model gpt-4o-mini
  tune config apikey sk-...
`, version)
}
