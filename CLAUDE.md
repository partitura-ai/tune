# Tune — AI-Powered Command Output Filter

## What is Tune?

Tune is a CLI tool that sits between commands and AI agents, using a fast LLM to strip noise from command output and return only what matters. Think of it as an AI-native version of `grep`/`awk`/`sed` but with semantic understanding.

**Example**: `go test -v ./...` produces 25,000 tokens of verbose output. If all tests pass, the agent only needs to see "PASS" (1 token). If 2 tests fail, the agent needs the failed test names, file paths, line numbers, and error messages (~40 tokens). Tune does this automatically.

**Why**: AI agents running commands waste massive context window on noisy stdout. Every token of `=== RUN TestFoo ... --- PASS` that goes into the context is a token that could be used for reasoning. Tune typically saves 90-100% of tokens on passing commands.

## Architecture

```
Agent calls Run("go test -v ./...")
  → tune intercepts (shell function wraps the real binary)
  → runs `command go test -v ./...` (bypasses function, calls real binary)
  → captures stdout+stderr + exit code
  → writes full raw output to ~/.tune/tee/<timestamp>_<cmd>.log
  → sends output to LLM (gpt-oss-20b via Groq on OpenRouter, ~600ms)
  → LLM returns only essential info
  → prints filtered output + [full output: /path/to/tee.log] reference
  → exits with original command's exit code
```

### Key Design Decisions

- **Go binary** — single binary, no dependencies, fast startup, embeds into Partitura
- **OpenRouter + Groq provider** — gpt-oss-20b is free on OpenRouter, Groq inference is ~600ms
- **Exit code preservation** — tune always exits with the original command's exit code, regardless of filter success/failure
- **Tee files** — full raw output always written to `~/.tune/tee/` so the agent can inspect it if the filtered output isn't enough. Last 50 files kept, auto-cleaned.
- **Graceful fallback** — if filter fails (network, timeout, API error), prints raw output with original exit code
- **Small output bypass** — outputs under 20 tokens (~80 chars) are printed directly without filtering
- **Stats tracking** — cumulative token savings stored in `~/.tune/stats.json`
- **Command registration** — `tune add go git` + `eval "$(tune init)"` makes `go` and `git` route through tune transparently

## Usage

```bash
# Direct usage
tune <command>                       # Filter command output
tune -i "find errors" <command>      # Explicit intent
tune -p <command>                    # Passthrough: live output + filtered summary

# Command registration (transparent interception)
tune add go git npm                  # Register commands
tune remove go                       # Unregister
tune list                            # Show registered commands
tune init                            # Output shell functions for eval
eval "$(tune init)"                  # Activate in current shell

# Stats
tune gain                            # Show token savings
tune gain --history                  # Show command history
```

### Shell setup (one-time)

```bash
tune add go git npm make             # Register commands to intercept
echo 'eval "$(tune init)"' >> ~/.zshrc   # Auto-activate on shell start
```

After this, `go test ./...` transparently routes through tune. The shell function calls `tune command go test ./...`, and tune uses `command go` internally to bypass the function and call the real binary.

## How to Build

```bash
go build -o tune .
cp tune ~/bin/tune    # or wherever your PATH points
```

## How to Test

```bash
go test ./...
```

Tests should be real — run actual commands through the filter with real API calls. No mocks. Use `OPENROUTER_API_KEY` env var.

## Project Structure

```
main.go                Entry point
cmd/root.go            CLI: argument parsing, command execution, tee files,
                       intent inference, command registration init
filter/filter.go       LLM filter: OpenRouter API call, response parsing
stats/stats.go         Token savings tracking (~/.tune/stats.json)
registry/registry.go   Command registration (~/.tune/commands.json)
```

### Key directories at runtime

```
~/.tune/
  stats.json           Cumulative token savings data
  commands.json        Registered commands for interception
  tee/                 Full raw output logs (last 50 kept)
    1773376243877_go_test__v__.log
    1773376255075_git_diff__stat.log
```

## Environment Variables

| Variable | Purpose |
|----------|---------|
| `TUNE_API_KEY` | OpenRouter API key (preferred) |
| `OPENROUTER_API_KEY` | Fallback API key |

## Integration with Partitura

Tune is designed to be embedded in Partitura's agent tool execution pipeline. When an agent calls `Run("go test ./...")` via MCP, Partitura wraps the command with `tune` before executing it in the tmux session. The agent receives the filtered output as the tool result.

Integration points in Partitura:
- `internal/mcp/endpoints/run.go` — wraps commands with `tune` when available
- `internal/mcp/endpoints/debug.go` — wraps debug commands
- Agent config: per-agent toggle for tune filtering

## Coding Guidelines

- **Go only** — no CGo, no external dependencies beyond stdlib
- **No mocks in tests** — tests hit real OpenRouter API
- **Single file per concern** — keep it simple, this is a small tool
- **Exit codes matter** — always preserve the original command's exit code, unconditionally. The exit code must reflect the wrapped command, never tune's own status.
- **Tee everything** — always write raw output to `~/.tune/tee/` before filtering. The agent must always be able to access the unfiltered output.
- **Fail open** — if anything goes wrong with filtering, print raw output and exit with original code
- **Speed matters** — this adds latency to every command. Target <1s filter time.
- **Token counting** — use `len(text) / 4` as rough estimate (same as RTK)
- **Shell safety** — `tune init` emits functions that use `command <binary>` to bypass the function and call the real binary. Never create infinite recursion.

## Planned Features

### Intent inference improvements
The `inferIntent()` function in `cmd/root.go` maps command patterns to intents. This should grow to cover more commands and be smarter about what the agent actually needs.

### Streaming mode
For long-running commands (builds, deploys), stream output in real-time but still produce a filtered summary at the end. The `-p` flag does basic passthrough but the summary isn't appended yet.

### Configuration system (NEXT PRIORITY)
`tune config` subcommand for managing models, providers, and API keys. Config stored at `~/.tune/config.json`.

```bash
tune config                          # Show current config
tune config provider                 # Interactive provider selection
tune config model                    # Set model for current provider
tune config apikey                   # Set API key for current provider

# Supported providers:
#   openrouter  — OpenRouter (default, supports gpt-oss-20b via Groq, gemini-flash, etc.)
#   openai      — OpenAI directly (gpt-4o-mini, etc.)
#   anthropic   — Anthropic directly (haiku, etc.)
#   gemini      — Google Gemini directly (gemini-2.5-flash, etc.)
#   ollama      — Local Ollama (qwen3.5:2b, etc. — free, offline, slower)
```

Config schema:
```json
{
  "provider": "openrouter",
  "model": "openai/gpt-oss-20b",
  "openrouter_provider": "groq",
  "api_keys": {
    "openrouter": "sk-or-...",
    "openai": "sk-...",
    "anthropic": "sk-ant-...",
    "gemini": "AIza..."
  },
  "max_input": 16000
}
```

API keys should also be loadable from env vars (`TUNE_API_KEY`, `OPENROUTER_API_KEY`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GEMINI_API_KEY`) as fallbacks. Config file takes precedence.

The `filter/filter.go` package needs to support multiple provider backends (OpenRouter, OpenAI, Anthropic, Gemini, Ollama) — each with their own API URL, auth header format, and response parsing. Keep it simple: one function per provider, selected by config.

### Hook integration
Tune can be installed as a Claude Code hook to automatically filter all Bash tool output. This would make it transparent — the agent doesn't even know Tune exists, it just gets cleaner output.

### Multi-command awareness
Track recent command outputs to provide better context. If the agent ran `go test` and it failed, then ran `go test` again after a fix, Tune could diff the outputs and show only what changed.

### Per-command intent config
Allow setting default intents per command in the registry, e.g. `tune add --intent "check for errors" npm` so that `npm run build` always uses that intent without `-i`.

## Performance Benchmarks

| Scenario | Raw Tokens | Filtered | Savings | Filter Time |
|----------|-----------|----------|---------|-------------|
| `go test -v` (25k lines, pass) | 24,546 | 1 | 100% | 749ms |
| `go test -v` (pass, 4k lines) | 4,149 | 1 | 100% | 807ms |
| `go test -v` (2 failures) | 134 | 38 | 72% | 568ms |
| `git diff --stat` | 219 | 13 | 94% | 750ms |

Average overhead: ~700ms per command via Groq. For commands that produce >100 tokens of output, the token savings far outweigh the latency cost.

### Model comparison (from prototype testing)

| Model | Location | Avg Latency | Accuracy | Notes |
|-------|----------|-------------|----------|-------|
| gpt-oss-20b (Groq) | OpenRouter | ~600ms | Excellent | Production choice |
| qwen3.5:2b | Local Ollama | ~3s | Good | Offline fallback |
| qwen3:0.6b | Local Ollama | ~400ms | Poor | Too small for output filtering |
| granite4:350m | Local Ollama | ~120ms | Poor | Can't follow instructions |
