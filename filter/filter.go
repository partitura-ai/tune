package filter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	OpenRouterURL = "https://openrouter.ai/api/v1/chat/completions"
	DefaultModel  = "openai/gpt-oss-20b"
	DefaultProvider = "groq"

	systemPrompt = `You are a command output filter for AI coding agents. Your job is to compress verbose command output to ONLY what the agent needs to act on.

CRITICAL rules:
- If exit code is 0 (success): respond with ONLY "PASS" or "ok" (one line)
- If exit code is non-zero (failure): you MUST include every error message, failed test name, file path, and line number — these are essential for debugging
- Strip: progress bars, timing info, download logs, PASS test output, "=== RUN" lines for passing tests
- Keep EXACTLY: failed test names, assertion errors, compiler errors, stack traces, file:line references
- Do NOT add your own commentary — output ONLY the filtered result
- Do NOT wrap in markdown code blocks`
)

type Config struct {
	APIKey   string
	Model    string
	Provider string
	MaxInput int // max chars of raw output to send to LLM
}

func DefaultConfig() Config {
	return Config{
		Model:    DefaultModel,
		Provider: DefaultProvider,
		MaxInput: 16000,
	}
}

type Result struct {
	Filtered   string
	RawLen     int
	FilterLen  int
	FilterTime time.Duration
}

func Filter(ctx context.Context, cfg Config, rawOutput string, exitCode int, intent string) (*Result, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("no API key configured (set OPENROUTER_API_KEY or TUNE_API_KEY)")
	}

	truncated := rawOutput
	if len(truncated) > cfg.MaxInput {
		truncated = truncated[:cfg.MaxInput]
	}

	userMsg := fmt.Sprintf("Intent: %s\nExit code: %d\nRaw output (%d chars):\n\n%s",
		intent, exitCode, len(rawOutput), truncated)

	body := map[string]any{
		"model": cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userMsg},
		},
		"temperature": 0.0,
		"max_tokens":  1024,
		"provider":    map[string]any{"order": []string{cfg.Provider}},
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", OpenRouterURL, bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	filterTime := time.Since(start)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("empty response from API")
	}

	filtered := result.Choices[0].Message.Content

	return &Result{
		Filtered:   filtered,
		RawLen:     len(rawOutput),
		FilterLen:  len(filtered),
		FilterTime: filterTime,
	}, nil
}
