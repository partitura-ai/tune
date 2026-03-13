# Tune

**Don't let the noise waste your tokens... let's tune outputs to be just what your AI agents need.**

Tune is an AI-powered command output filter. It sits between your commands and AI agents, using a fast LLM to strip noise from command output and return only what matters.

Made by [Gabriel Ferreira Angelo](https://github.com/Gabriel-Feang), creator of [Partitura](https://partitura-ai.com), to optimise token consumption on multi-agent teams. If you are a Software Engineer, make sure to check it out as well! You can also use Partitura 100% for free!

## Why?

AI agents running commands waste massive context window on noisy stdout. Every token of `=== RUN TestFoo ... --- PASS` that goes into the context is a token that could be used for reasoning.

```
go test -v ./...  →  25,000 tokens of verbose output
                  →  Tune: "PASS" (1 token)
```

If 2 tests fail, Tune returns only the failed test names, file paths, line numbers, and error messages (~40 tokens). **90-100% token savings on passing commands.**

## Install

### Homebrew

```bash
brew install partitura-ai/tap/tune
```

### From source

```bash
go install github.com/partitura-ai/tune@latest
```

### Build locally

```bash
git clone https://github.com/partitura-ai/tune.git
cd tune
go build -o tune .
cp tune /usr/local/bin/tune
```

## Quick Start

```bash
# Set up your API key (OpenRouter is the default — free models available)
export OPENROUTER_API_KEY="sk-or-..."

# Register commands for transparent interception
tune add go git npm make
eval "$(tune init)"

# Now just use your commands as normal — they route through Tune automatically
go test -v ./...    # → filtered output
git diff --stat     # → filtered output
```

## Usage

```bash
# Direct usage
tune <command>                       # Filter command output
tune -i "find errors" <command>      # Explicit intent
tune -p <command>                    # Passthrough: live output + filtered summary
tune -s <command>                    # Stream: filter in real-time chunks

# Image filtering
tune image screenshot.png            # Extract info from screenshot
tune image -i "find errors" img.png  # Image with explicit intent

# Stats
tune gain                            # Show token savings
tune gain --history                  # Show command history
```

### Shell setup (one-time)

```bash
tune add go git npm make             # Register commands to intercept
echo 'eval "$(tune init)"' >> ~/.zshrc   # Auto-activate on shell start
```

After this, `go test ./...` transparently routes through Tune.

## Configuration

```bash
tune config                          # Show current config
tune config provider <name>          # Set provider
tune config model <model>            # Set model
tune config apikey <key>             # Set API key for current provider
```

### Providers

| Provider | Setup | Best for |
|----------|-------|----------|
| **openrouter** (default) | `OPENROUTER_API_KEY` | Fast & cheap — gpt-oss-20b via Groq (~600ms) |
| **ollama** | Install [Ollama](https://ollama.com), `ollama pull qwen3.5:9b` | Free, offline, private |
| **openai** | `OPENAI_API_KEY` | If you already have an OpenAI key |
| **anthropic** | `ANTHROPIC_API_KEY` | If you already have an Anthropic key |
| **gemini** | `GEMINI_API_KEY` | If you already have a Gemini key |

### Recommended: Ollama (free, local)

```bash
# Install Ollama: https://ollama.com
ollama pull qwen3.5:9b

tune config provider ollama
tune config model qwen3.5:9b
```

No API key needed. Runs 100% locally. Slower (~3s) but completely free and private.

## Cost Comparison

The real value of Tune: **every token you filter out is a token your expensive reasoning model doesn't have to process.**

### Example: `go test -v ./...` (all tests pass)

| Scenario | Raw output | After Tune | Cost to process |
|----------|-----------|------------|-----------------|
| Without Tune → Claude Opus 4.6 | 25,000 tokens | — | **$0.375** ($15/M input) |
| Without Tune → Claude Sonnet 4 | 25,000 tokens | — | **$0.075** ($3/M input) |
| Tune (OpenRouter gpt-oss-20b) → Claude Opus 4.6 | 25,000 tokens | 1 token | **~$0.00** (filter free + 1 token) |
| Tune (Ollama qwen3.5:9b) → Claude Opus 4.6 | 25,000 tokens | 1 token | **$0.00** (fully local) |

### Example: `go test -v ./...` (2 failures)

| Scenario | Raw output | After Tune | Cost to process |
|----------|-----------|------------|-----------------|
| Without Tune → Claude Opus 4.6 | 25,000 tokens | — | **$0.375** |
| Tune (OpenRouter) → Claude Opus 4.6 | 25,000 tokens | ~40 tokens | **~$0.00** |

### Per-command filter cost

| Provider | Model | Latency | Cost per filter |
|----------|-------|---------|-----------------|
| OpenRouter | gpt-oss-20b (Groq) | ~600ms | Free (gpt-oss-20b is free on OpenRouter) |
| Ollama | qwen3.5:9b | ~3s | Free (local) |
| OpenAI | gpt-4o-mini | ~800ms | ~$0.002 |

**Bottom line**: Tune costs practically nothing to run but saves $0.05-$0.40 per command when using expensive models. In a multi-agent team running dozens of commands, that adds up fast.

## Performance

| Scenario | Raw Tokens | Filtered | Savings | Filter Time |
|----------|-----------|----------|---------|-------------|
| `go test -v` (25k lines, pass) | 24,546 | 1 | 100% | 749ms |
| `go test -v` (pass, 4k lines) | 4,149 | 1 | 100% | 807ms |
| `go test -v` (2 failures) | 134 | 38 | 72% | 568ms |
| `git diff --stat` | 219 | 13 | 94% | 750ms |

## Environment Variables

| Variable | Purpose |
|----------|---------|
| `TUNE_API_KEY` | API key for active provider (universal fallback) |
| `OPENROUTER_API_KEY` | OpenRouter API key |
| `OPENAI_API_KEY` | OpenAI API key |
| `ANTHROPIC_API_KEY` | Anthropic API key |
| `GEMINI_API_KEY` | Gemini API key |

## License

MIT
