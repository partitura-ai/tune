package filter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/partitura-ai/tune/config"
)

const systemPrompt = `You are a command output filter for AI coding agents. Your job is to compress verbose command output to ONLY what the agent needs to act on.

CRITICAL rules:
- If exit code is 0 (success): respond with ONLY "PASS" or "ok" (one line)
- If exit code is non-zero (failure): you MUST include every error message, failed test name, file path, and line number — these are essential for debugging
- Strip: progress bars, timing info, download logs, PASS test output, "=== RUN" lines for passing tests
- Keep EXACTLY: failed test names, assertion errors, compiler errors, stack traces, file:line references
- Do NOT add your own commentary — output ONLY the filtered result
- Do NOT wrap in markdown code blocks`

const imageSystemPrompt = `You are a command output filter for AI coding agents. You are looking at a screenshot. Your job is to extract ONLY the essential information the agent needs.

CRITICAL rules:
- Extract all text content visible in the image
- Focus on: error messages, file paths, line numbers, stack traces, status indicators
- If it's a passing/successful output, respond with ONLY "PASS" or "ok"
- If there are errors/failures, include every error message, file path, and line number
- Do NOT add your own commentary — output ONLY the extracted essential info
- Do NOT wrap in markdown code blocks`

type Result struct {
	Filtered   string
	RawLen     int
	FilterLen  int
	FilterTime time.Duration
}

// providerEndpoint returns the API URL for a given provider.
func providerEndpoint(provider string, cfg *config.Config) string {
	switch provider {
	case "openrouter":
		return "https://openrouter.ai/api/v1/chat/completions"
	case "openai":
		return "https://api.openai.com/v1/chat/completions"
	case "anthropic":
		return "https://api.anthropic.com/v1/messages"
	case "gemini":
		return "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"
	case "ollama":
		return "http://localhost:11434/api/chat"
	default:
		return "https://openrouter.ai/api/v1/chat/completions"
	}
}

// Filter sends command output to the configured LLM provider and returns filtered output.
func Filter(ctx context.Context, cfg *config.Config, rawOutput string, exitCode int, intent string) (*Result, error) {
	apiKey := cfg.ActiveAPIKey()
	if apiKey == "" && cfg.Provider != "ollama" {
		return nil, fmt.Errorf("no API key for provider %q (set via 'tune config apikey' or env var)", cfg.Provider)
	}

	truncated := rawOutput
	if len(truncated) > cfg.MaxInput {
		truncated = truncated[:cfg.MaxInput]
	}

	userMsg := fmt.Sprintf("Intent: %s\nExit code: %d\nRaw output (%d chars):\n\n%s",
		intent, exitCode, len(rawOutput), truncated)

	if cfg.Provider == "anthropic" {
		return filterAnthropic(ctx, cfg, apiKey, userMsg)
	}
	if cfg.Provider == "ollama" {
		return filterOllama(ctx, cfg, userMsg)
	}
	return filterOpenAICompat(ctx, cfg, apiKey, userMsg, nil)
}

// FilterImage sends an image to the configured vision-capable LLM with resolution backoff.
// It OCRs the full-res image first for text reference, then sends the lowest resolution
// image that produces a good response — escalating only if needed.
func FilterImage(ctx context.Context, cfg *config.Config, imageData []byte, mimeType string, intent string) (*Result, error) {
	apiKey := cfg.ActiveAPIKey()
	if apiKey == "" && cfg.Provider != "ollama" {
		return nil, fmt.Errorf("no API key for provider %q", cfg.Provider)
	}

	if intent == "" {
		intent = "extract essential information from this image"
	}

	// OCR the full-res image first — free text extraction the model can use
	// as reference if characters are hard to read at low resolution
	ocrText := ocrImage(imageData)

	// Resolution backoff: start small, increase if response is too brief
	resolutions := []int{256, 512, 1024, 2048}
	minAcceptableLen := 20

	var lastResult *Result
	for i, maxDim := range resolutions {
		resized, err := resizeImage(imageData, mimeType, maxDim)
		if err != nil {
			if i == 0 {
				resized = imageData
			} else {
				break
			}
		}

		// Build intent with OCR context if available
		fullIntent := intent
		if ocrText != "" {
			fullIntent = fmt.Sprintf("%s\n\nOCR text extracted from this image (use as reference for any hard-to-read characters):\n%s", intent, ocrText)
		}

		var result *Result
		switch cfg.Provider {
		case "anthropic":
			result, err = filterAnthropicImage(ctx, cfg, apiKey, resized, mimeType, fullIntent)
		case "ollama":
			result, err = filterOllamaImage(ctx, cfg, resized, fullIntent)
		default:
			result, err = filterOpenAICompatImage(ctx, cfg, apiKey, resized, mimeType, fullIntent)
		}
		if err != nil {
			if lastResult != nil {
				return lastResult, nil
			}
			return nil, err
		}

		lastResult = result

		if len(result.Filtered) >= minAcceptableLen || i == len(resolutions)-1 {
			return result, nil
		}
	}

	return lastResult, nil
}

// ---------------------------------------------------------------------------
// OpenAI-compatible providers (OpenRouter, OpenAI, Gemini)
// ---------------------------------------------------------------------------

func filterOpenAICompat(ctx context.Context, cfg *config.Config, apiKey string, userMsg string, imageContent []map[string]any) (*Result, error) {
	url := providerEndpoint(cfg.Provider, cfg)

	var messages []map[string]any
	if imageContent != nil {
		messages = []map[string]any{
			{"role": "system", "content": imageSystemPrompt},
			{"role": "user", "content": imageContent},
		}
	} else {
		messages = []map[string]any{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userMsg},
		}
	}

	body := map[string]any{
		"model":       cfg.Model,
		"messages":    messages,
		"temperature": 0.0,
		"max_tokens":  1024,
	}

	// OpenRouter-specific: provider routing
	if cfg.Provider == "openrouter" && cfg.OpenRouterProvider != "" {
		body["provider"] = map[string]any{"order": []string{cfg.OpenRouterProvider}}
	}

	return doOpenAIRequest(ctx, url, apiKey, body)
}

func filterOpenAICompatImage(ctx context.Context, cfg *config.Config, apiKey string, imageData []byte, mimeType string, intent string) (*Result, error) {
	b64 := encodeBase64(imageData)
	dataURL := fmt.Sprintf("data:%s;base64,%s", mimeType, b64)

	content := []map[string]any{
		{"type": "text", "text": fmt.Sprintf("Intent: %s", intent)},
		{"type": "image_url", "image_url": map[string]any{"url": dataURL, "detail": "low"}},
	}

	return filterOpenAICompat(ctx, cfg, cfg.ActiveAPIKey(), "", content)
}

func doOpenAIRequest(ctx context.Context, url, apiKey string, body map[string]any) (*Result, error) {
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

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
	rawLen := 0
	if msgs, ok := body["messages"].([]map[string]any); ok {
		for _, m := range msgs {
			if s, ok := m["content"].(string); ok {
				rawLen += len(s)
			}
		}
	}

	return &Result{
		Filtered:   filtered,
		RawLen:     rawLen,
		FilterLen:  len(filtered),
		FilterTime: filterTime,
	}, nil
}

// ---------------------------------------------------------------------------
// Anthropic Messages API
// ---------------------------------------------------------------------------

func filterAnthropic(ctx context.Context, cfg *config.Config, apiKey string, userMsg string) (*Result, error) {
	body := map[string]any{
		"model":      cfg.Model,
		"max_tokens": 1024,
		"system":     systemPrompt,
		"messages": []map[string]any{
			{"role": "user", "content": userMsg},
		},
	}

	return doAnthropicRequest(ctx, apiKey, body)
}

func filterAnthropicImage(ctx context.Context, cfg *config.Config, apiKey string, imageData []byte, mimeType string, intent string) (*Result, error) {
	b64 := encodeBase64(imageData)

	body := map[string]any{
		"model":      cfg.Model,
		"max_tokens": 1024,
		"system":     imageSystemPrompt,
		"messages": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{
						"type": "image",
						"source": map[string]any{
							"type":       "base64",
							"media_type": mimeType,
							"data":       b64,
						},
					},
					{
						"type": "text",
						"text": fmt.Sprintf("Intent: %s", intent),
					},
				},
			},
		},
	}

	return doAnthropicRequest(ctx, apiKey, body)
}

func doAnthropicRequest(ctx context.Context, apiKey string, body map[string]any) (*Result, error) {
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

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
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	if len(result.Content) == 0 {
		return nil, fmt.Errorf("empty response from API")
	}

	filtered := result.Content[0].Text
	return &Result{
		Filtered:   filtered,
		FilterLen:  len(filtered),
		FilterTime: filterTime,
	}, nil
}

// ---------------------------------------------------------------------------
// Ollama (local)
// ---------------------------------------------------------------------------

func filterOllama(ctx context.Context, cfg *config.Config, userMsg string) (*Result, error) {
	body := map[string]any{
		"model": cfg.Model,
		"messages": []map[string]any{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userMsg},
		},
		"stream": false,
	}

	return doOllamaRequest(ctx, body)
}

func filterOllamaImage(ctx context.Context, cfg *config.Config, imageData []byte, intent string) (*Result, error) {
	b64 := encodeBase64(imageData)

	body := map[string]any{
		"model": cfg.Model,
		"messages": []map[string]any{
			{"role": "system", "content": imageSystemPrompt},
			{
				"role":    "user",
				"content": fmt.Sprintf("Intent: %s", intent),
				"images":  []string{b64},
			},
		},
		"stream": false,
	}

	return doOllamaRequest(ctx, body)
}

func doOllamaRequest(ctx context.Context, body map[string]any) (*Result, error) {
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "http://localhost:11434/api/chat", bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Ollama request failed (is Ollama running?): %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	filterTime := time.Since(start)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Ollama error %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse Ollama response: %w", err)
	}

	filtered := result.Message.Content
	return &Result{
		Filtered:   filtered,
		FilterLen:  len(filtered),
		FilterTime: filterTime,
	}, nil
}
