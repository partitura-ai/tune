# Tune — AI-Powered Command Output Filter

## What is Tune?

Tune is a CLI tool that sits between commands and AI agents, using a fast LLM to strip noise from command output and return only what matters. Think of it as an AI-native version of `grep`/`awk`/`sed` but with semantic understanding.

**Example**: `go test -v ./...` produces 25,000 tokens of verbose output. If all tests pass, the agent only needs to see "PASS" (1 token). If 2 tests fail, the agent needs the failed test names, file paths, line numbers, and error messages (~40 tokens). Tune does this automatically.

**Why**: AI agents running commands waste massive context window on noisy stdout. Every token of `=== RUN TestFoo ... --- PASS` that goes into the context is a token that could be used for reasoning. Tune typically saves 90-100% of tokens on passing commands.

## Architecture

```
Agent calls Run("go test -v ./...")
  → tune intercepts the command
  → runs it, captures stdout+stderr + exit code
  → sends output to LLM (gpt-oss-20b via Groq on OpenRouter, ~600ms)
  → LLM returns only essential info
  → agent receives filtered output as the tool result
```

### Key Design Decisions

- **Go binary** — single binary, no dependencies, fast startup, embeds into Partitura
- **OpenRouter + Groq provider** — gpt-oss-20b is free on OpenRouter, Groq inference is ~600ms
- **Exit code preservation** — tune exits with the original command's exit code
- **Graceful fallback** — if filter fails (network, timeout, API error), prints raw output
- **Small output bypass** — outputs under 20 tokens (~80 chars) are printed directly without filtering
- **Stats tracking** — cumulative token savings stored in `~/.tune/stats.json`

## Usage

```bash
tune <command>                       # Filter command output
tune -i "find errors" <command>      # Explicit intent
tune -p <command>                    # Passthrough: live output + filtered summary
tune gain                            # Show token savings
tune gain --history                  # Show command history
```

## How to Build

```bash
go build -o tune .
```

## How to Test

```bash
go test ./...
```

Tests should be real — run actual commands through the filter with real API calls. No mocks. Use `OPENROUTER_API_KEY` env var.

## Project Structure

```
main.go              Entry point
cmd/root.go          CLI argument parsing, command execution, intent inference
filter/filter.go     LLM filter: sends output to OpenRouter, parses response
stats/stats.go       Token savings tracking, ~/.tune/stats.json persistence
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
- **Exit codes matter** — always preserve the original command's exit code
- **Fail open** — if anything goes wrong with filtering, print raw output
- **Speed matters** — this adds latency to every command. Target <1s filter time.
- **Token counting** — use `len(text) / 4` as rough estimate (same as RTK)

## Planned Features

### Intent inference improvements
The `inferIntent()` function in `cmd/root.go` maps command patterns to intents. This should grow to cover more commands and be smarter about what the agent actually needs.

### Streaming mode
For long-running commands (builds, deploys), stream output in real-time but still produce a filtered summary at the end. The `-p` flag does basic passthrough but the summary isn't appended yet.

### Configurable models
Allow switching between local Ollama (qwen3.5:2b for offline/free) and OpenRouter (gpt-oss-20b for speed/quality). Config file at `~/.tune/config.json`.

### Hook integration
Tune can be installed as a Claude Code hook to automatically filter all Bash tool output. This would make it transparent — the agent doesn't even know Tune exists, it just gets cleaner output.

### Multi-command awareness
Track recent command outputs to provide better context. If the agent ran `go test` and it failed, then ran `go test` again after a fix, Tune could diff the outputs and show only what changed.

## Performance Benchmarks (from prototype testing)

| Scenario | Raw Tokens | Filtered | Savings | Filter Time |
|----------|-----------|----------|---------|-------------|
| `go test -v` (25k lines, pass) | 24,546 | 1 | 100% | 749ms |
| `go test -v` (pass, 4k lines) | 4,149 | 1 | 100% | 807ms |
| `go test -v` (2 failures) | 134 | 38 | 72% | 568ms |
| `git diff --stat` | 219 | 13 | 94% | 750ms |

Average overhead: ~700ms per command. For commands that produce >100 tokens of output, the token savings far outweigh the latency cost.
