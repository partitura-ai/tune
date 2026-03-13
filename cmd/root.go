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

	"github.com/charmbracelet/lipgloss"
	"github.com/partitura-ai/tune/config"
	"github.com/partitura-ai/tune/filter"
	"github.com/partitura-ai/tune/progress"
	"github.com/partitura-ai/tune/registry"
	"github.com/partitura-ai/tune/stats"
	"github.com/partitura-ai/tune/ui"
)

const version = "1.2.0"

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
		fmt.Printf("  %s %s\n", ui.Success.Render("✓"), cmd)
	}
	if err := reg.Save(); err != nil {
		return err
	}

	// Auto-install eval line into shell profile if not already there
	ensureShellInit()
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
		fmt.Printf("  %s %s\n", ui.Warning.Render("✗"), cmd)
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
		fmt.Println(ui.Faint.Render("No commands registered. Use 'tune add <command>' to register one."))
		return nil
	}
	fmt.Println(ui.Subtitle.Render("Registered commands"))
	for _, cmd := range cmds {
		fmt.Printf("  %s %s\n", ui.Success.Render("•"), cmd)
	}
	return nil
}

// ---------------------------------------------------------------------------
// ensureShellInit — add eval line to shell profile if missing
// ---------------------------------------------------------------------------

func ensureShellInit() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	// Detect shell profile
	shell := os.Getenv("SHELL")
	var profilePath string
	switch {
	case strings.Contains(shell, "zsh"):
		profilePath = filepath.Join(home, ".zshrc")
	case strings.Contains(shell, "bash"):
		// Prefer .bashrc, fall back to .bash_profile
		profilePath = filepath.Join(home, ".bashrc")
		if _, err := os.Stat(profilePath); os.IsNotExist(err) {
			profilePath = filepath.Join(home, ".bash_profile")
		}
	default:
		profilePath = filepath.Join(home, ".zshrc")
	}

	initLine := `eval "$(tune init)"`

	// Check if already present
	data, err := os.ReadFile(profilePath)
	if err == nil && strings.Contains(string(data), "tune init") {
		fmt.Printf("\n  Shell init already in %s\n", profilePath)
		fmt.Printf("  Restart your shell or run: eval \"$(tune init)\"\n")
		return
	}

	// Append
	f, err := os.OpenFile(profilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n  Could not write to %s: %v\n", profilePath, err)
		fmt.Printf("  Add manually: %s\n", initLine)
		return
	}
	defer f.Close()

	fmt.Fprintf(f, "\n# Tune — AI command output filter\n%s\n", initLine)
	fmt.Printf("\n  Added to %s\n", profilePath)
	fmt.Printf("  Restart your shell or run: eval \"$(tune init)\"\n")
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
unalias %s 2>/dev/null
%s() {
  %s command %s "$@"
}
`, cmd, cmd, cmd, tuneBin, cmd)
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
			fmt.Println(ui.Subtitle.Render("Current provider: ") + ui.Value.Render(cfg.Provider))
			fmt.Println(ui.Faint.Render("Available: " + strings.Join(config.Providers(), ", ")))
			return nil
		}
		p := args[1]
		if !config.ValidProvider(p) {
			return fmt.Errorf("unknown provider %q — available: %s", p, strings.Join(config.Providers(), ", "))
		}
		cfg.Provider = p
		// Set sensible default model per provider
		switch p {
		case "openrouter":
			cfg.Model = "openai/gpt-oss-20b"
			cfg.OpenRouterProvider = "groq"
		case "anthropic":
			cfg.Model = "claude-haiku-4-5-20251001"
		case "gemini":
			cfg.Model = "gemini-2.5-flash-lite"
		case "openai":
			cfg.Model = "gpt-5-nano"
		case "ollama":
			cfg.Model = selectOllamaModel(cfg.Model)
		}
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("%s Provider set to: %s (model: %s)\n", ui.Success.Render("✓"), p, cfg.Model)

	case "model":
		if len(args) < 2 {
			fmt.Println(ui.Subtitle.Render("Current model: ") + ui.Value.Render(cfg.Model))
			return nil
		}
		cfg.Model = args[1]
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("%s Model set to: %s\n", ui.Success.Render("✓"), args[1])

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
		fmt.Printf("%s API key set for: %s\n", ui.Success.Render("✓"), provider)

	case "or-provider":
		if len(args) < 2 {
			fmt.Printf("Current OpenRouter provider: %s\n", cfg.OpenRouterProvider)
			return nil
		}
		cfg.OpenRouterProvider = args[1]
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("%s OpenRouter provider set to: %s\n", ui.Success.Render("✓"), args[1])

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
		fmt.Printf("%s Max input set to: %d chars\n", ui.Success.Render("✓"), n)

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
		fmt.Printf("%s Stream mode: %v\n", ui.Success.Render("✓"), cfg.Stream)

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
		fmt.Fprintf(os.Stderr, "<🎼> filter error: %v\n", filterErr)
		fmt.Print(rawOutput)
		os.Exit(exitCode)
	}

	if teeFile != "" {
		fmt.Fprintf(os.Stderr, "<🎼> %s\n", teeFile)
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
		fmt.Fprintf(os.Stderr, "<🎼> chunked filter error: %v\n", filterErr)
	}

	if teeFile != "" {
		fmt.Fprintf(os.Stderr, "<🎼> %s\n", teeFile)
	}

	if result != nil {
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

// selectOllamaModel runs `ollama ls` and lets the user pick a model, or returns fallback.
func selectOllamaModel(fallback string) string {
	out, err := exec.Command("ollama", "ls").CombinedOutput()
	if err != nil {
		fmt.Println(ui.Warning.Render("Could not list Ollama models (is Ollama running?)"))
		if fallback != "" {
			return fallback
		}
		return "qwen3.5:9b"
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) <= 1 {
		fmt.Println(ui.Warning.Render("No Ollama models found. Pull one with: ollama pull qwen3.5:9b"))
		return "qwen3.5:9b"
	}

	// Parse model names from `ollama ls` output (first column)
	var models []string
	for _, line := range lines[1:] { // skip header
		fields := strings.Fields(line)
		if len(fields) > 0 {
			models = append(models, fields[0])
		}
	}

	if len(models) == 0 {
		return "qwen3.5:9b"
	}

	fmt.Println(ui.Subtitle.Render("Available Ollama models"))
	for i, m := range models {
		fmt.Printf("  %s %s\n", ui.Accent.Render(fmt.Sprintf("[%d]", i+1)), m)
	}
	fmt.Print(ui.Faint.Render("Select model (number or name) [1]: "))

	var input string
	fmt.Scanln(&input)
	input = strings.TrimSpace(input)

	if input == "" {
		return models[0]
	}

	// Try as number
	var idx int
	if _, err := fmt.Sscanf(input, "%d", &idx); err == nil && idx >= 1 && idx <= len(models) {
		return models[idx-1]
	}

	// Try as model name (exact or prefix match)
	for _, m := range models {
		if strings.HasPrefix(m, input) {
			return m
		}
	}

	return input
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

// ---------------------------------------------------------------------------
// Usage
// ---------------------------------------------------------------------------

func printUsage() {
	title := ui.Title.Render(fmt.Sprintf("🎼 tune %s", version))
	subtitle := lipgloss.NewStyle().Bold(true).Foreground(ui.White).Render("AI-powered command output filter")
	slogan := ui.Slogan.Render("Don't let the noise waste your tokens... let's tune outputs to be just what your AI agents need.")

	fmt.Println(title + "  " + subtitle)
	fmt.Println(slogan)
	fmt.Println()

	section := func(name string) string {
		return ui.Subtitle.Render(name)
	}
	cmd := func(c, desc string) string {
		return fmt.Sprintf("  %s  %s", lipgloss.NewStyle().Foreground(ui.White).Render(c), ui.Faint.Render(desc))
	}
	envLine := func(name, desc string) string {
		return fmt.Sprintf("  %s  %s", ui.Value.Render(name), ui.Faint.Render(desc))
	}

	fmt.Println(section("Usage"))
	fmt.Println(cmd("tune <command>                  ", "Run command, print filtered output"))
	fmt.Println(cmd("tune -i \"intent\" <command>      ", "Run with explicit intent"))
	fmt.Println(cmd("tune -p <command>               ", "Passthrough: show live + filtered summary"))
	fmt.Println(cmd("tune -s <command>               ", "Stream: filter in real-time chunks"))
	fmt.Println(cmd("tune image <image-path>         ", "Filter/extract info from image"))
	fmt.Println(cmd("tune gain                       ", "Show token savings stats"))
	fmt.Println(cmd("tune gain --history             ", "Show command history"))
	fmt.Println()

	fmt.Println(section("Configuration"))
	fmt.Println(cmd("tune config                     ", "Show current config"))
	fmt.Println(cmd("tune config provider <name>     ", "Set provider (openrouter, openai, anthropic, gemini, ollama)"))
	fmt.Println(cmd("tune config model <model>       ", "Set model"))
	fmt.Println(cmd("tune config apikey <key>        ", "Set API key for current provider"))
	fmt.Println(cmd("tune config apikey <prov> <key> ", "Set API key for specific provider"))
	fmt.Println(cmd("tune config or-provider <name>  ", "Set OpenRouter sub-provider (e.g. groq)"))
	fmt.Println(cmd("tune config max-input <chars>   ", "Set max input chars"))
	fmt.Println()

	fmt.Println(section("Command Registration"))
	fmt.Println(cmd("tune add <command>              ", "Register a command for interception"))
	fmt.Println(cmd("tune remove <command>           ", "Unregister a command"))
	fmt.Println(cmd("tune list                       ", "Show registered commands"))
	fmt.Println(cmd("tune init                       ", "Output shell functions (eval this)"))
	fmt.Println()

	fmt.Println(section("Quick Start"))
	fmt.Println(cmd("tune add go git npm             ", "Register commands"))
	fmt.Println(cmd("eval \"$(tune init)\"             ", "Activate in current shell"))
	fmt.Println()

	fmt.Println(section("Environment"))
	fmt.Println(envLine("TUNE_API_KEY        ", "API key for active provider"))
	fmt.Println(envLine("OPENROUTER_API_KEY  ", "OpenRouter API key"))
	fmt.Println(envLine("OPENAI_API_KEY      ", "OpenAI API key"))
	fmt.Println(envLine("ANTHROPIC_API_KEY   ", "Anthropic API key"))
	fmt.Println(envLine("GEMINI_API_KEY      ", "Gemini API key"))
	fmt.Println()

	credit := ui.Faint.Render("Made by Gabriel Ferreira Angelo, creator of ") +
		lipgloss.NewStyle().Foreground(ui.Cyan).Underline(true).Render("https://partitura-ai.com")
	fmt.Println(credit)
	fmt.Println(ui.Faint.Render("To optimise token consumption on multi-agent teams."))
	fmt.Println(ui.Faint.Render("If you are a Software Engineer, make sure to check it out as well!"))
	fmt.Println(ui.Faint.Render("You can also use Partitura 100% for free!"))
}
