package filter

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/partitura-ai/tune/config"
)

const chunkSystemPrompt = `You are a command output filter for AI coding agents. You are seeing a CHUNK of ongoing command output (the command is still running).

CRITICAL rules:
- If this chunk contains ONLY passing tests or routine output: respond with nothing (empty response)
- If this chunk contains failures, errors, or warnings: output ONLY the error messages, file paths, line numbers, and failed test names
- Strip: progress bars, timing info, "=== RUN" lines, "--- PASS" lines, download logs
- Keep EXACTLY: "--- FAIL" lines, assertion errors, compiler errors, stack traces, file:line references
- Do NOT add commentary, do NOT say "no errors" — just output errors or nothing
- Do NOT wrap in markdown code blocks`

const finalChunkSystemPrompt = `You are a command output filter for AI coding agents. This is the FINAL chunk of command output. The command has finished.

CRITICAL rules:
- If exit code is 0 AND no errors in this chunk: respond with ONLY "PASS"
- If exit code is non-zero OR there are errors: output every error message, failed test name, file path, and line number
- If errors were already reported in previous chunks, just report any NEW errors in this chunk, plus a one-line summary like "N total failures"
- Strip: progress bars, timing info, "=== RUN" lines, "--- PASS" lines
- Keep EXACTLY: failed test names, assertion errors, compiler errors, stack traces, file:line references
- Do NOT add commentary — output ONLY the filtered result
- Do NOT wrap in markdown code blocks`

// ChunkedConfig controls when chunks are flushed to the LLM.
type ChunkedConfig struct {
	FlushInterval time.Duration // max time to accumulate before flushing
	FlushBytes    int           // max bytes to accumulate before flushing
}

func DefaultChunkedConfig() ChunkedConfig {
	return ChunkedConfig{
		FlushInterval: 3 * time.Second,
		FlushBytes:    8192, // ~2k tokens
	}
}

// ChunkedResult is the final result of a chunked filter run.
type ChunkedResult struct {
	FilteredOutput string        // all filtered output concatenated
	RawLen         int           // total raw bytes from command
	FilterLen      int           // total filtered bytes
	FilterTime     time.Duration // total time spent in LLM calls
	ChunkCount     int           // number of chunks sent to LLM
}

// FilterChunked reads from the command's output pipe, batches it into chunks,
// and filters each chunk through the LLM — streaming results to w in real-time.
// The caller provides the command's stdout/stderr pipe as reader.
// exitCodeCh should receive the exit code once the command finishes.
func FilterChunked(
	ctx context.Context,
	cfg *config.Config,
	cc ChunkedConfig,
	reader io.Reader,
	w io.Writer,
	exitCodeCh <-chan int,
) (*ChunkedResult, error) {
	apiKey := cfg.ActiveAPIKey()
	if apiKey == "" && cfg.Provider != "ollama" {
		return nil, fmt.Errorf("no API key for provider %q", cfg.Provider)
	}

	var (
		mu          sync.Mutex
		buf         strings.Builder
		totalRaw    int
		totalFilter int
		totalTime   time.Duration
		chunkCount  int
		allFiltered strings.Builder
		prevErrors  string // summary of errors from previous chunks
	)

	// flushChunk sends the current buffer to the LLM and streams the response.
	// isFinal indicates whether the command has finished.
	flushChunk := func(exitCode int, isFinal bool) {
		mu.Lock()
		chunk := buf.String()
		buf.Reset()
		mu.Unlock()

		if chunk == "" && !isFinal {
			return
		}

		totalRaw += len(chunk)
		chunkCount++

		// Truncate chunk if too large
		truncated := chunk
		if len(truncated) > cfg.MaxInput {
			truncated = truncated[:cfg.MaxInput]
		}

		var sysPrompt string
		var userMsg string
		if isFinal {
			sysPrompt = finalChunkSystemPrompt
			userMsg = fmt.Sprintf("Exit code: %d\nPrevious errors reported: %s\nFinal output chunk (%d chars):\n\n%s",
				exitCode, prevErrorsSummary(prevErrors), len(chunk), truncated)
		} else {
			sysPrompt = chunkSystemPrompt
			userMsg = fmt.Sprintf("Ongoing output chunk (%d chars):\n\n%s", len(chunk), truncated)
		}

		start := time.Now()
		filtered, err := streamChunk(ctx, cfg, apiKey, sysPrompt, userMsg, w)
		elapsed := time.Since(start)
		totalTime += elapsed

		if err != nil {
			// On filter error, print raw chunk
			fmt.Fprint(w, chunk)
			totalFilter += len(chunk)
			allFiltered.WriteString(chunk)
			return
		}

		if filtered != "" {
			if !strings.HasSuffix(filtered, "\n") {
				fmt.Fprintln(w)
			}
			totalFilter += len(filtered)
			allFiltered.WriteString(filtered)
			prevErrors += filtered + "\n"
		}
	}

	// Read output line by line, accumulating into buffer
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	timer := time.NewTimer(cc.FlushInterval)
	defer timer.Stop()

	lineCh := make(chan string)

	go func() {
		defer close(lineCh)
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
	}()

	for {
		select {
		case line, ok := <-lineCh:
			if !ok {
				// Scanner done — wait for exit code and do final flush
				exitCode := <-exitCodeCh
				flushChunk(exitCode, true)
				return &ChunkedResult{
					FilteredOutput: allFiltered.String(),
					RawLen:         totalRaw,
					FilterLen:      totalFilter,
					FilterTime:     totalTime,
					ChunkCount:     chunkCount,
				}, nil
			}
			mu.Lock()
			buf.WriteString(line)
			buf.WriteByte('\n')
			size := buf.Len()
			mu.Unlock()

			// Flush on size threshold
			if size >= cc.FlushBytes {
				timer.Reset(cc.FlushInterval)
				flushChunk(0, false)
			}

		case <-timer.C:
			// Flush on time threshold
			mu.Lock()
			hasData := buf.Len() > 0
			mu.Unlock()
			if hasData {
				flushChunk(0, false)
			}
			timer.Reset(cc.FlushInterval)

		case <-ctx.Done():
			return &ChunkedResult{
				FilteredOutput: allFiltered.String(),
				RawLen:         totalRaw,
				FilterLen:      totalFilter,
				FilterTime:     totalTime,
				ChunkCount:     chunkCount,
			}, ctx.Err()
		}
	}
}

func prevErrorsSummary(prev string) string {
	if prev == "" {
		return "none"
	}
	// Truncate to keep the prompt small
	if len(prev) > 500 {
		return prev[:500] + "..."
	}
	return prev
}

// streamChunk sends a single chunk to the LLM with streaming and returns the full response.
func streamChunk(ctx context.Context, cfg *config.Config, apiKey string, sysPrompt string, userMsg string, w io.Writer) (string, error) {
	switch cfg.Provider {
	case "anthropic":
		return streamChunkAnthropic(ctx, cfg, apiKey, sysPrompt, userMsg, w)
	case "ollama":
		return streamChunkOllama(ctx, cfg, sysPrompt, userMsg, w)
	default:
		return streamChunkOpenAI(ctx, cfg, apiKey, sysPrompt, userMsg, w)
	}
}

// Provider-specific streaming for chunks — reuse the SSE parsing logic from stream.go
// but with custom system prompts.

func streamChunkOpenAI(ctx context.Context, cfg *config.Config, apiKey string, sysPrompt string, userMsg string, w io.Writer) (string, error) {
	url := providerEndpoint(cfg.Provider, cfg)

	body := map[string]any{
		"model": cfg.Model,
		"messages": []map[string]any{
			{"role": "system", "content": sysPrompt},
			{"role": "user", "content": userMsg},
		},
		"temperature": 0.0,
		"max_tokens":  1024,
		"stream":      true,
	}
	if cfg.Provider == "openrouter" && cfg.OpenRouterProvider != "" {
		body["provider"] = map[string]any{"order": []string{cfg.OpenRouterProvider}}
	}

	return doSSEStream(ctx, url, apiKey, "Bearer", body, w, parseOpenAIDelta)
}

func streamChunkAnthropic(ctx context.Context, cfg *config.Config, apiKey string, sysPrompt string, userMsg string, w io.Writer) (string, error) {
	body := map[string]any{
		"model":      cfg.Model,
		"max_tokens": 1024,
		"system":     sysPrompt,
		"stream":     true,
		"messages": []map[string]any{
			{"role": "user", "content": userMsg},
		},
	}

	return doSSEStream(ctx, "https://api.anthropic.com/v1/messages", apiKey, "x-api-key", body, w, parseAnthropicDelta)
}

func streamChunkOllama(ctx context.Context, cfg *config.Config, sysPrompt string, userMsg string, w io.Writer) (string, error) {
	body := map[string]any{
		"model": cfg.Model,
		"messages": []map[string]any{
			{"role": "system", "content": sysPrompt},
			{"role": "user", "content": userMsg},
		},
		"stream": true,
	}

	return doOllamaStream(ctx, body, w)
}
