package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"

	"github.com/partitura-ai/tune/config"
	"github.com/partitura-ai/tune/fastvlm"
	"github.com/partitura-ai/tune/filter"
	"github.com/partitura-ai/tune/progress"
	"github.com/partitura-ai/tune/registry"
	"github.com/partitura-ai/tune/stats"
	"github.com/partitura-ai/tune/ui"
)

const version = "1.4.1"

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
	case "reset":
		return cmdReset()
	case "image", "img":
		return cmdImage(args[1:])
	case "enable":
		return cmdEnable()
	case "disable":
		return cmdDisable()
	case "fastvlm":
		return cmdFastVLM(args[1:])
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

func cmdConfigInteractive(cfg *config.Config) error {
	fmt.Println(ui.Title.Render("🎼 Tune Setup"))
	fmt.Println()

	// --- 1. Provider ---
	providers := config.Providers()
	defaultIdx := 0
	for i, p := range providers {
		if p == cfg.Provider {
			defaultIdx = i
			break
		}
	}
	fmt.Println(ui.Subtitle.Render("Provider"))
	for i, p := range providers {
		marker := "  "
		if i == defaultIdx {
			marker = ui.Success.Render("→ ")
		}
		fmt.Printf("%s%s %s\n", marker, ui.Accent.Render(fmt.Sprintf("[%d]", i+1)), p)
	}
	fmt.Printf(ui.Faint.Render("Select [%d]: "), defaultIdx+1)

	var input string
	fmt.Scanln(&input)
	input = strings.TrimSpace(input)
	if input != "" {
		var idx int
		if _, err := fmt.Sscanf(input, "%d", &idx); err == nil && idx >= 1 && idx <= len(providers) {
			cfg.Provider = providers[idx-1]
		} else {
			// Try as name
			for _, p := range providers {
				if strings.EqualFold(p, input) {
					cfg.Provider = p
					break
				}
			}
		}
	}
	fmt.Printf("  %s %s\n\n", ui.Success.Render("✓"), cfg.Provider)

	// --- 2. API Key (if needed and not already set) ---
	if cfg.Provider != "ollama" && cfg.Provider != "fastvlm" {
		existingKey := cfg.ActiveAPIKey()
		if existingKey == "" {
			envVar := config.EnvVarForProvider(cfg.Provider)
			fmt.Println(ui.Subtitle.Render("API Key"))
			if envVar != "" {
				fmt.Printf(ui.Faint.Render("  No key found (checked %s env var)\n"), envVar)
			}
			fmt.Print(ui.Faint.Render("Paste API key: "))
			fmt.Scanln(&input)
			input = strings.TrimSpace(input)
			if input != "" {
				cfg.APIKeys[cfg.Provider] = input
				fmt.Printf("  %s Key set\n\n", ui.Success.Render("✓"))
			} else {
				fmt.Printf("  %s Skipped (set later with 'tune config apikey <key>')\n\n", ui.Warning.Render("!"))
			}
		} else {
			fmt.Printf("%s  %s\n\n", ui.Faint.Render("API Key:"), ui.Success.Render(config.MaskKey(existingKey)))
		}
	}

	// --- 3. Model ---
	defaultModel := cfg.Model
	if defaultModel == "" {
		defaultModel = config.DefaultModelForProvider(cfg.Provider)
	}
	fmt.Println(ui.Subtitle.Render("Model"))
	if cfg.Provider == "ollama" {
		cfg.Model = selectOllamaModel(defaultModel)
	} else {
		fmt.Printf(ui.Faint.Render("Model [%s]: "), defaultModel)
		fmt.Scanln(&input)
		input = strings.TrimSpace(input)
		if input != "" {
			cfg.Model = input
		} else {
			cfg.Model = defaultModel
		}
	}
	fmt.Printf("  %s %s\n\n", ui.Success.Render("✓"), cfg.Model)

	// --- 4. Timeout ---
	fmt.Println(ui.Subtitle.Render("Timeout"))
	fmt.Printf(ui.Faint.Render("Filter timeout in seconds [%d]: "), cfg.Timeout)
	fmt.Scanln(&input)
	input = strings.TrimSpace(input)
	if input != "" {
		var n int
		if _, err := fmt.Sscanf(input, "%d", &n); err == nil && n >= 1 {
			cfg.Timeout = n
		}
	}
	fmt.Printf("  %s %ds\n\n", ui.Success.Render("✓"), cfg.Timeout)

	// --- 5. Stream ---
	fmt.Println(ui.Subtitle.Render("Streaming"))
	streamDefault := "on"
	if !cfg.Stream {
		streamDefault = "off"
	}
	fmt.Printf(ui.Faint.Render("Stream mode on/off [%s]: "), streamDefault)
	fmt.Scanln(&input)
	input = strings.TrimSpace(strings.ToLower(input))
	switch input {
	case "off", "false", "no":
		cfg.Stream = false
	case "on", "true", "yes":
		cfg.Stream = true
	}
	streamLabel := "on"
	if !cfg.Stream {
		streamLabel = "off"
	}
	fmt.Printf("  %s %s\n\n", ui.Success.Render("✓"), streamLabel)

	// --- Save ---
	if err := cfg.Save(); err != nil {
		return err
	}

	fmt.Println(ui.Success.Render("✓ Configuration saved"))
	fmt.Println(ui.Faint.Render("  " + filepath.Join(os.Getenv("HOME"), ".tune", "config.json")))
	return nil
}

func cmdConfig(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if len(args) == 0 {
		return cmdConfigInteractive(cfg)
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
		case "fastvlm":
			cfg.Model = "fastvlm-1.5b"
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

	case "timeout":
		if len(args) < 2 {
			fmt.Printf("Current timeout: %ds\n", cfg.Timeout)
			return nil
		}
		var n int
		if _, err := fmt.Sscanf(args[1], "%d", &n); err != nil || n < 1 {
			return fmt.Errorf("invalid timeout: must be a number >= 1 (seconds)")
		}
		cfg.Timeout = n
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("%s Timeout set to: %ds\n", ui.Success.Render("✓"), n)

	default:
		return fmt.Errorf("unknown config key %q — use: provider, model, apikey, or-provider, max-input, stream, timeout", args[0])
	}
	return nil
}

// ---------------------------------------------------------------------------
// tune reset — remove all tune config and shell integration
// ---------------------------------------------------------------------------

func cmdReset() error {
	fmt.Println(ui.Title.Render("🎼 Tune Reset"))
	fmt.Println()

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	tuneDir := filepath.Join(home, ".tune")

	// Show what will be removed
	fmt.Println(ui.Subtitle.Render("This will remove:"))
	fmt.Printf("  %s %s\n", ui.Warning.Render("•"), tuneDir)

	// Find shell profiles with tune init
	shellProfiles := []string{
		filepath.Join(home, ".zshrc"),
		filepath.Join(home, ".bashrc"),
		filepath.Join(home, ".bash_profile"),
	}
	var affectedProfiles []string
	for _, p := range shellProfiles {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if strings.Contains(string(data), "tune init") {
			affectedProfiles = append(affectedProfiles, p)
			fmt.Printf("  %s tune init lines from %s\n", ui.Warning.Render("•"), p)
		}
	}

	fmt.Println()
	fmt.Print(ui.Warning.Render("Are you sure? [y/N] "))

	var input string
	fmt.Scanln(&input)
	input = strings.TrimSpace(strings.ToLower(input))
	if input != "y" && input != "yes" {
		fmt.Println(ui.Faint.Render("Cancelled."))
		return nil
	}

	// Remove tune init lines from shell profiles
	for _, p := range affectedProfiles {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")
		var cleaned []string
		for _, line := range lines {
			// Skip the comment line and the eval line
			if strings.Contains(line, "Tune — AI command output filter") || strings.Contains(line, "tune init") {
				continue
			}
			cleaned = append(cleaned, line)
		}
		if err := os.WriteFile(p, []byte(strings.Join(cleaned, "\n")), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "  %s Could not clean %s: %v\n", ui.Warning.Render("!"), p, err)
		} else {
			fmt.Printf("  %s Cleaned %s\n", ui.Success.Render("✓"), p)
		}
	}

	// Remove ~/.tune directory
	if err := os.RemoveAll(tuneDir); err != nil {
		fmt.Fprintf(os.Stderr, "  %s Could not remove %s: %v\n", ui.Warning.Render("!"), tuneDir, err)
	} else {
		fmt.Printf("  %s Removed %s\n", ui.Success.Render("✓"), tuneDir)
	}

	fmt.Println()
	fmt.Println(ui.Success.Render("✓ Tune has been reset. Restart your shell to complete."))
	return nil
}

// ---------------------------------------------------------------------------
// tune enable / disable — toggle filtering globally
// ---------------------------------------------------------------------------

func cmdEnable() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.SetEnabled(true)
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("%s Tune filtering enabled\n", ui.Success.Render("✓"))
	return nil
}

func cmdDisable() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.SetEnabled(false)
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("%s Tune filtering disabled (commands pass through unfiltered)\n", ui.Warning.Render("!"))
	return nil
}

// ---------------------------------------------------------------------------
// tune fastvlm — setup/status/remove FastVLM Claude Code hook
// ---------------------------------------------------------------------------

const hookScript = `#!/usr/bin/env bash
# Tune image hook — intercepts Claude Code Read calls on image files.
# Uses tune CLI to process images via OpenRouter (cheap, high quality)
# with FastVLM as local fallback.
# Managed by: tune fastvlm setup

if ! command -v jq &>/dev/null; then
  exit 0
fi

INPUT=$(cat)
FILE_PATH=$(echo "$INPUT" | jq -r '.tool_input.file_path // empty')

if [ -z "$FILE_PATH" ]; then
  exit 0
fi

# Check if file is an image by extension (case-insensitive)
EXT="${FILE_PATH##*.}"
EXT=$(echo "$EXT" | tr '[:upper:]' '[:lower:]')
case "$EXT" in
  png|jpg|jpeg|gif|webp|bmp|tiff|tif)
    ;;
  *)
    exit 0
    ;;
esac

# Check file actually exists
if [ ! -f "$FILE_PATH" ]; then
  exit 0
fi

TUNE="$HOME/bin/tune"
FASTVLM="$HOME/bin/fastvlm-cli"
INTENT="Describe this image comprehensively for a coding AI agent that cannot see it. Include: 1) Full OCR of all visible text, preserving layout and indentation 2) Visual context: what the image shows (terminal, browser, UI, diagram, etc), layout, colors 3) Actionable details: error messages, file paths, line numbers, URLs, button labels, status indicators. Be thorough and preserve exact text."

# Try OpenRouter first (Gemini Flash Lite — cheap + high quality)
if [ -x "$TUNE" ]; then
  DESCRIPTION=$("$TUNE" image -i "$INTENT" --provider openrouter --model google/gemini-2.5-flash-lite --timeout 10 "$FILE_PATH" 2>/dev/null)
  if [ -n "$DESCRIPTION" ]; then
    jq -n --arg desc "$DESCRIPTION" '{
      "hookSpecificOutput": {
        "hookEventName": "PreToolUse",
        "permissionDecision": "deny",
        "permissionDecisionReason": ("[tune:gemini-flash-lite] " + $desc)
      }
    }'
    exit 0
  fi
fi

# Fallback: FastVLM (local, offline)
if [ -x "$FASTVLM" ]; then
  DESCRIPTION=$("$FASTVLM" "$FILE_PATH" \
    --prompt "$INTENT" \
    --model-path "$HOME/ml-fastvlm/app/FastVLM/model" \
    --max-tokens 240 \
    2>/dev/null)
  if [ -n "$DESCRIPTION" ]; then
    jq -n --arg desc "$DESCRIPTION" '{
      "hookSpecificOutput": {
        "hookEventName": "PreToolUse",
        "permissionDecision": "deny",
        "permissionDecisionReason": ("[tune:fastvlm] " + $desc)
      }
    }'
    exit 0
  fi
fi

# Both failed — let Claude read the image normally
exit 0
`

func cmdFastVLM(args []string) error {
	if len(args) == 0 {
		return cmdFastVLMStatus()
	}

	switch args[0] {
	case "setup":
		return cmdFastVLMSetup()
	case "status":
		return cmdFastVLMStatus()
	case "remove":
		return cmdFastVLMRemove()
	default:
		return fmt.Errorf("unknown fastvlm subcommand %q — use: setup, status, remove", args[0])
	}
}

func cmdFastVLMStatus() error {
	home, _ := os.UserHomeDir()
	fmt.Println(ui.Title.Render("🚀 FastVLM Status"))
	fmt.Println()

	// Check binary
	bin := filepath.Join(home, "bin", "fastvlm-cli")
	if _, err := os.Stat(bin); err == nil {
		fmt.Printf("  %s fastvlm-cli binary: %s\n", ui.Success.Render("✓"), bin)
	} else {
		fmt.Printf("  %s fastvlm-cli binary not found at %s\n", ui.Warning.Render("✗"), bin)
		fmt.Printf("    Build: cd ~/fastvlm-cli && swift build -c release && cp .build/release/fastvlm-cli ~/bin/\n")
	}

	// Check metallib
	metallib := filepath.Join(home, "bin", "mlx.metallib")
	if _, err := os.Stat(metallib); err == nil {
		fmt.Printf("  %s Metal shaders: %s\n", ui.Success.Render("✓"), metallib)
	} else {
		fmt.Printf("  %s Metal shaders not found at %s\n", ui.Warning.Render("✗"), metallib)
	}

	// Check model
	modelDir := filepath.Join(home, "ml-fastvlm/app/FastVLM/model")
	configFile := filepath.Join(modelDir, "config.json")
	if _, err := os.Stat(configFile); err == nil {
		fmt.Printf("  %s Model: %s\n", ui.Success.Render("✓"), modelDir)
	} else {
		fmt.Printf("  %s Model not found at %s\n", ui.Warning.Render("✗"), modelDir)
		fmt.Printf("    Download: cd ~/ml-fastvlm/app && ./get_pretrained_mlx_model.sh --model 1.5b --dest FastVLM/model\n")
	}

	// Check hook script
	hookPath := filepath.Join(home, ".claude", "hooks", "tune-image.sh")
	if data, err := os.ReadFile(hookPath); err == nil {
		if strings.Contains(string(data), "fastvlm-cli") {
			fmt.Printf("  %s Hook script: %s (fastvlm)\n", ui.Success.Render("✓"), hookPath)
		} else {
			fmt.Printf("  %s Hook script exists but not using fastvlm: %s\n", ui.Warning.Render("!"), hookPath)
		}
	} else {
		fmt.Printf("  %s Hook script not found: %s\n", ui.Warning.Render("✗"), hookPath)
	}

	// Check Claude Code settings
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if data, err := os.ReadFile(settingsPath); err == nil {
		if strings.Contains(string(data), "tune-image.sh") {
			fmt.Printf("  %s Claude Code hook registered\n", ui.Success.Render("✓"))
		} else {
			fmt.Printf("  %s Claude Code hook not registered in settings.json\n", ui.Warning.Render("✗"))
		}
	} else {
		fmt.Printf("  %s Claude Code settings not found: %s\n", ui.Warning.Render("✗"), settingsPath)
	}

	fmt.Println()
	fmt.Println(ui.Faint.Render("  Run 'tune fastvlm setup' to install/fix everything"))
	return nil
}

func cmdFastVLMSetup() error {
	home, _ := os.UserHomeDir()
	fmt.Println(ui.Title.Render("🚀 FastVLM Setup"))
	fmt.Println(ui.Faint.Render("  Installing FastVLM for local Neural Engine image processing"))
	fmt.Println()

	binDir := filepath.Join(home, "bin")
	bin := filepath.Join(binDir, "fastvlm-cli")
	metallib := filepath.Join(binDir, "mlx.metallib")
	projectDir := filepath.Join(home, "fastvlm-cli")
	repoDir := filepath.Join(home, "ml-fastvlm")
	modelDir := filepath.Join(repoDir, "app", "FastVLM", "model")

	// Step 1: Check prerequisites
	fmt.Println(ui.Subtitle.Render("Checking prerequisites"))

	if err := runQuiet("xcode-select", "-p"); err != nil {
		return fmt.Errorf("Xcode Command Line Tools not installed.\n  Run: xcode-select --install")
	}
	fmt.Printf("  %s Xcode Command Line Tools\n", ui.Success.Render("✓"))

	if err := runQuiet("swift", "--version"); err != nil {
		return fmt.Errorf("Swift not found. Install Xcode or Xcode Command Line Tools.")
	}
	fmt.Printf("  %s Swift compiler\n", ui.Success.Render("✓"))

	if err := runQuiet("xcrun", "-sdk", "macosx", "metal", "--version"); err != nil {
		fmt.Printf("  %s Metal toolchain not found, downloading...\n", ui.Warning.Render("!"))
		if err := runVisible("xcodebuild", "-downloadComponent", "MetalToolchain"); err != nil {
			return fmt.Errorf("failed to download Metal toolchain: %w", err)
		}
	}
	fmt.Printf("  %s Metal toolchain\n", ui.Success.Render("✓"))
	fmt.Println()

	// Step 2: Clone ml-fastvlm repo (for model download script + mlpackage)
	fmt.Println(ui.Subtitle.Render("Model repository"))

	if _, err := os.Stat(filepath.Join(repoDir, "app")); err != nil {
		fmt.Printf("  Cloning ml-fastvlm...\n")
		if err := runVisible("git", "clone", "https://github.com/apple/ml-fastvlm.git", repoDir); err != nil {
			return fmt.Errorf("failed to clone ml-fastvlm: %w", err)
		}
	}
	fmt.Printf("  %s ml-fastvlm repo: %s\n", ui.Success.Render("✓"), repoDir)

	// Step 3: Download 1.5B model
	if _, err := os.Stat(filepath.Join(modelDir, "config.json")); err != nil {
		fmt.Printf("  Downloading 1.5B model (~2GB)...\n")
		if err := downloadModel(repoDir, modelDir); err != nil {
			return fmt.Errorf("model download failed: %w", err)
		}
	}
	fmt.Printf("  %s Model: %s\n", ui.Success.Render("✓"), modelDir)
	fmt.Println()

	// Step 4: Write Swift CLI source (embedded in tune binary)
	fmt.Println(ui.Subtitle.Render("Building fastvlm-cli"))

	if err := writeSwiftSources(projectDir); err != nil {
		return fmt.Errorf("failed to write Swift sources: %w", err)
	}
	fmt.Printf("  %s Swift sources written to %s\n", ui.Success.Render("✓"), projectDir)

	// Step 5: Build
	if _, err := os.Stat(bin); err != nil {
		fmt.Printf("  Building (this takes a few minutes on first run)...\n")
		cmd := exec.Command("swift", "build", "-c", "release")
		cmd.Dir = projectDir
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("swift build failed: %w", err)
		}

		// Copy binary
		if err := os.MkdirAll(binDir, 0755); err != nil {
			return err
		}
		buildBin := filepath.Join(projectDir, ".build", "release", "fastvlm-cli")
		if err := copyFile(buildBin, bin); err != nil {
			return fmt.Errorf("failed to install binary: %w", err)
		}
	}
	fmt.Printf("  %s fastvlm-cli: %s\n", ui.Success.Render("✓"), bin)

	// Step 6: Compile Metal shaders into metallib
	if _, err := os.Stat(metallib); err != nil {
		fmt.Printf("  Compiling Metal shaders...\n")
		if err := compileMetallib(projectDir, metallib); err != nil {
			return fmt.Errorf("metallib compilation failed: %w", err)
		}
	}
	fmt.Printf("  %s Metal shaders: %s\n", ui.Success.Render("✓"), metallib)
	fmt.Println()

	// Step 7: Write hook script
	fmt.Println(ui.Subtitle.Render("Claude Code integration"))

	hookDir := filepath.Join(home, ".claude", "hooks")
	if err := os.MkdirAll(hookDir, 0755); err != nil {
		return fmt.Errorf("failed to create hooks dir: %w", err)
	}
	hookPath := filepath.Join(hookDir, "tune-image.sh")
	if err := os.WriteFile(hookPath, []byte(hookScript), 0755); err != nil {
		return fmt.Errorf("failed to write hook script: %w", err)
	}
	fmt.Printf("  %s Hook script: %s\n", ui.Success.Render("✓"), hookPath)

	// Step 8: Register hook in Claude Code settings
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := ensureClaudeCodeHook(settingsPath, hookPath); err != nil {
		fmt.Printf("  %s Could not update Claude Code settings: %v\n", ui.Warning.Render("!"), err)
		fmt.Printf("    Add manually to %s\n", settingsPath)
	} else {
		fmt.Printf("  %s Claude Code hook registered\n", ui.Success.Render("✓"))
	}

	fmt.Println()
	fmt.Println(ui.Success.Render("✓ FastVLM is ready"))
	fmt.Println(ui.Faint.Render("  Every Claude Code image Read now goes through FastVLM (~5s, local Neural Engine)"))
	fmt.Println(ui.Faint.Render("  Test: tune image --provider fastvlm ~/Desktop/screenshot.png"))
	fmt.Println(ui.Faint.Render("  Remove: tune fastvlm remove"))
	return nil
}

// ---------------------------------------------------------------------------
// FastVLM setup helpers
// ---------------------------------------------------------------------------

func runQuiet(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

func runVisible(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0755)
}

func writeSwiftSources(projectDir string) error {
	srcDir := filepath.Join(projectDir, "Sources", "fastvlm-cli")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		return err
	}

	files := map[string][]byte{
		filepath.Join(projectDir, "Package.swift"):                     fastvlm.PackageSwift,
		filepath.Join(srcDir, "FastVLM.swift"):                         fastvlm.FastVLMSwift,
		filepath.Join(srcDir, "FastVLMCLI.swift"):                      fastvlm.FastVLMCLISwift,
		filepath.Join(srcDir, "MediaProcessingExtensions.swift"):       fastvlm.MediaProcessingExtensionsSwift,
	}

	for path, content := range files {
		if err := os.WriteFile(path, content, 0644); err != nil {
			return fmt.Errorf("writing %s: %w", filepath.Base(path), err)
		}
	}
	return nil
}

func downloadModel(_, modelDir string) error {
	modelURL := "https://ml-site.cdn-apple.com/datasets/fastvlm/llava-fastvithd_1.5b_stage3_llm.int8.zip"

	if err := os.MkdirAll(modelDir, 0755); err != nil {
		return err
	}

	tmpDir, err := os.MkdirTemp("", "fastvlm-model-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	zipPath := filepath.Join(tmpDir, "model.zip")

	// Download with curl (available on all macOS)
	fmt.Fprintf(os.Stderr, "  Downloading from Apple CDN...\n")
	if err := runVisible("curl", "-L", "--progress-bar", "-o", zipPath, modelURL); err != nil {
		return fmt.Errorf("download failed: %w", err)
	}

	// Extract
	fmt.Fprintf(os.Stderr, "  Extracting...\n")
	extractDir := filepath.Join(tmpDir, "extracted")
	if err := os.MkdirAll(extractDir, 0755); err != nil {
		return err
	}
	if err := runVisible("unzip", "-q", zipPath, "-d", extractDir); err != nil {
		return fmt.Errorf("unzip failed: %w", err)
	}

	// The zip contains a directory named like the model — find and copy contents
	entries, err := os.ReadDir(extractDir)
	if err != nil {
		return err
	}
	srcDir := extractDir
	for _, e := range entries {
		if e.IsDir() {
			srcDir = filepath.Join(extractDir, e.Name())
			break
		}
	}

	// Copy all files from extracted dir to model dir
	modelEntries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}
	for _, e := range modelEntries {
		src := filepath.Join(srcDir, e.Name())
		dst := filepath.Join(modelDir, e.Name())
		if e.IsDir() {
			// Copy directory recursively (for mlpackage)
			if err := runQuiet("cp", "-r", src, dst); err != nil {
				return fmt.Errorf("copying %s: %w", e.Name(), err)
			}
		} else {
			data, err := os.ReadFile(src)
			if err != nil {
				return err
			}
			if err := os.WriteFile(dst, data, 0644); err != nil {
				return err
			}
		}
	}

	return nil
}

func compileMetallib(projectDir, outputPath string) error {
	metalDir := filepath.Join(projectDir, ".build", "checkouts", "mlx-swift", "Source", "Cmlx", "mlx-generated", "metal")
	kernelsDir := filepath.Join(projectDir, ".build", "checkouts", "mlx-swift", "Source", "Cmlx", "mlx", "mlx", "backend", "metal", "kernels")

	// Find all .metal files
	var metalFiles []string
	err := filepath.Walk(metalDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".metal") {
			metalFiles = append(metalFiles, path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("finding metal files: %w", err)
	}

	if len(metalFiles) == 0 {
		return fmt.Errorf("no .metal files found in %s (run swift build first)", metalDir)
	}

	tmpDir, err := os.MkdirTemp("", "mlx-air-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	// Compile each .metal to .air
	for _, f := range metalFiles {
		base := strings.TrimSuffix(filepath.Base(f), ".metal")
		airPath := filepath.Join(tmpDir, base+".air")
		if err := runQuiet("xcrun", "-sdk", "macosx", "metal", "-c", f,
			"-I", kernelsDir, "-I", filepath.Dir(kernelsDir),
			"-o", airPath); err != nil {
			return fmt.Errorf("compiling %s: %w", filepath.Base(f), err)
		}
	}

	// Link all .air into metallib
	airFiles, _ := filepath.Glob(filepath.Join(tmpDir, "*.air"))
	args := append([]string{"-sdk", "macosx", "metallib"}, airFiles...)
	args = append(args, "-o", outputPath)
	if err := runQuiet("xcrun", args...); err != nil {
		return fmt.Errorf("linking metallib: %w", err)
	}

	return nil
}

func cmdFastVLMRemove() error {
	home, _ := os.UserHomeDir()
	fmt.Println(ui.Title.Render("🚀 FastVLM Remove"))
	fmt.Println()

	// Remove hook script
	hookPath := filepath.Join(home, ".claude", "hooks", "tune-image.sh")
	if err := os.Remove(hookPath); err == nil {
		fmt.Printf("  %s Removed hook script: %s\n", ui.Success.Render("✓"), hookPath)
	} else if !os.IsNotExist(err) {
		fmt.Printf("  %s Could not remove %s: %v\n", ui.Warning.Render("!"), hookPath, err)
	}

	// Remove hook from Claude Code settings
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := removeClaudeCodeHook(settingsPath); err != nil {
		fmt.Printf("  %s Could not update Claude Code settings: %v\n", ui.Warning.Render("!"), err)
	} else {
		fmt.Printf("  %s Claude Code hook unregistered\n", ui.Success.Render("✓"))
	}

	fmt.Println()
	fmt.Println(ui.Success.Render("✓ FastVLM hook removed"))
	fmt.Println(ui.Faint.Render("  Claude Code will read images normally (raw pixels to model)"))
	return nil
}

// ensureClaudeCodeHook adds the tune-image.sh hook to Claude Code settings if not already present.
func ensureClaudeCodeHook(settingsPath, hookPath string) error {
	var settings map[string]any

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			settings = map[string]any{}
		} else {
			return err
		}
	} else {
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("failed to parse settings.json: %w", err)
		}
	}

	// Check if hook already registered
	if strings.Contains(string(data), "tune-image.sh") {
		return nil // already there
	}

	// Build the hook entry
	hookEntry := map[string]any{
		"matcher": "Read",
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": hookPath,
			},
		},
	}

	// Get or create hooks.PreToolUse
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		settings["hooks"] = hooks
	}

	preToolUse, _ := hooks["PreToolUse"].([]any)
	preToolUse = append(preToolUse, hookEntry)
	hooks["PreToolUse"] = preToolUse

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath, out, 0644)
}

// removeClaudeCodeHook removes the tune-image.sh hook from Claude Code settings.
func removeClaudeCodeHook(settingsPath string) error {
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return nil // no settings file, nothing to remove
	}

	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return err
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return nil
	}

	preToolUse, _ := hooks["PreToolUse"].([]any)
	if preToolUse == nil {
		return nil
	}

	// Filter out entries that reference tune-image.sh
	var filtered []any
	for _, entry := range preToolUse {
		entryJSON, _ := json.Marshal(entry)
		if !strings.Contains(string(entryJSON), "tune-image.sh") {
			filtered = append(filtered, entry)
		}
	}

	if len(filtered) == 0 {
		delete(hooks, "PreToolUse")
	} else {
		hooks["PreToolUse"] = filtered
	}

	if len(hooks) == 0 {
		delete(settings, "hooks")
	}

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath, out, 0644)
}

// ---------------------------------------------------------------------------
// tune image — filter image with resolution backoff
// ---------------------------------------------------------------------------

func cmdImage(args []string) error {
	intent := ""
	providerOverride := ""
	modelOverride := ""
	timeoutOverride := 0
	var imagePath string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--intent", "-i":
			if i+1 < len(args) {
				i++
				intent = args[i]
			}
		case "--provider":
			if i+1 < len(args) {
				i++
				providerOverride = args[i]
			}
		case "--model":
			if i+1 < len(args) {
				i++
				modelOverride = args[i]
			}
		case "--timeout", "-t":
			if i+1 < len(args) {
				i++
				fmt.Sscanf(args[i], "%d", &timeoutOverride)
			}
		default:
			imagePath = args[i]
		}
	}

	if imagePath == "" {
		return fmt.Errorf("usage: tune image [-i intent] [--provider <provider>] [--model <model>] [--timeout <seconds>] <image-path>")
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

	if providerOverride != "" {
		cfg.Provider = providerOverride
	}
	if modelOverride != "" {
		cfg.Model = modelOverride
	}

	timeout := cfg.Timeout
	if timeoutOverride > 0 {
		timeout = timeoutOverride
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
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

func isTerminal(fd uintptr) bool {
	return term.IsTerminal(int(fd))
}

func runPassthrough(args []string) {
	cmd := exec.Command("sh", "-c", strings.Join(args, " "))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Run()
	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	os.Exit(exitCode)
}

func cmdFilter(args []string) error {
	cfgCheck, err := config.Load()
	if err == nil && !cfgCheck.IsEnabled() {
		runPassthrough(args)
	}

	// No TTY on stderr — non-interactive context (e.g. vcs_info, scripts).
	// Skip filtering to avoid polluting programmatic callers.
	if !isTerminal(os.Stderr.Fd()) {
		runPassthrough(args)
	}

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

	if cfg.ActiveAPIKey() == "" && cfg.Provider != "ollama" && cfg.Provider != "fastvlm" {
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

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Timeout)*time.Second)
	defer cancel()

	var result *filter.Result
	var filterErr error

	result, filterErr = filter.Filter(ctx, cfg, rawOutput, exitCode, intent)
	if filterErr != nil {
		fmt.Fprintf(os.Stderr, "tune: filter error: %v\n", filterErr)
		fmt.Print(rawOutput)
		os.Exit(exitCode)
	}

	printTuneResult(result.Filtered, exitCode, teeFile)

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

	preview := progress.NewPreview(3)
	cc := filter.DefaultChunkedConfig()
	cc.OnLine = func(line string) {
		preview.AddLine(line)
	}
	cc.OnFlush = func() {
		preview.Clear()
	}
	result, filterErr := filter.FilterChunked(context.Background(), cfg, cc, teeReader, io.Discard, exitCodeCh)
	preview.Clear()

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
		fmt.Fprintf(os.Stderr, "tune: filter error: %v\n", filterErr)
	}

	if result != nil && result.FilteredOutput != "" {
		printTuneResult(result.FilteredOutput, exitCode, teeFile)
		teeFile = "" // already printed
	}

	if teeFile != "" {
		printTeeLine(teeFile)
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

// printTuneResult formats and prints the filtered output with ✔︎/✖︎ prefix and tee file reference.
func printTuneResult(filtered string, exitCode int, teeFile string) {
	filtered = strings.TrimSpace(filtered)
	if filtered == "" {
		return
	}

	symbol := "✔︎"
	if exitCode != 0 {
		symbol = "✖︎"
	}

	// First line gets the symbol prefix
	lines := strings.Split(filtered, "\n")
	fmt.Fprintf(os.Stderr, " ┗ %s %s\n", symbol, lines[0])
	for _, line := range lines[1:] {
		fmt.Fprintf(os.Stderr, "   %s\n", line)
	}

	if teeFile != "" {
		fmt.Fprintf(os.Stderr, "     [complete output at `%s`]\n", teeFile)
	}
}

// printTeeLine prints just the tee file reference (when result was already printed or on error fallback).
func printTeeLine(teeFile string) {
	fmt.Fprintf(os.Stderr, "     [complete output at `%s`]\n", teeFile)
}

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
	fmt.Println(cmd("tune enable                     ", "Enable filtering (default)"))
	fmt.Println(cmd("tune disable                    ", "Disable filtering (passthrough)"))
	fmt.Println(cmd("tune fastvlm setup              ", "Install FastVLM hook for Claude Code images"))
	fmt.Println(cmd("tune fastvlm status             ", "Check FastVLM installation status"))
	fmt.Println(cmd("tune fastvlm remove             ", "Remove FastVLM hook"))
	fmt.Println()

	fmt.Println(section("Configuration"))
	fmt.Println(cmd("tune config                     ", "Show current config"))
	fmt.Println(cmd("tune config provider <name>     ", "Set provider (openrouter, openai, anthropic, gemini, ollama, fastvlm)"))
	fmt.Println(cmd("tune config model <model>       ", "Set model"))
	fmt.Println(cmd("tune config apikey <key>        ", "Set API key for current provider"))
	fmt.Println(cmd("tune config apikey <prov> <key> ", "Set API key for specific provider"))
	fmt.Println(cmd("tune config or-provider <name>  ", "Set OpenRouter sub-provider (e.g. groq)"))
	fmt.Println(cmd("tune config max-input <chars>   ", "Set max input chars"))
	fmt.Println(cmd("tune config timeout <seconds>   ", "Set filter timeout (default: 5s)"))
	fmt.Println()

	fmt.Println(section("Command Registration"))
	fmt.Println(cmd("tune add <command>              ", "Register a command for interception"))
	fmt.Println(cmd("tune remove <command>           ", "Unregister a command"))
	fmt.Println(cmd("tune list                       ", "Show registered commands"))
	fmt.Println(cmd("tune init                       ", "Output shell functions (eval this)"))
	fmt.Println(cmd("tune reset                      ", "Remove all tune config and shell integration"))
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
	fmt.Println()
	fmt.Println(ui.Slogan.Render("~Hope you enjoy this tiny piece of what Partitura can do."))
}
