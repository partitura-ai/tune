package filter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/partitura-ai/tune/config"
)

const systemPrompt = `You are a command output filter for AI coding agents. Your job is to compress verbose command output to ONLY what the agent needs to act on.

CRITICAL rules:
- If exit code is 0 (success): respond with a ONE-LINE summary with key metrics. Examples:
  "47 tests pass across 10 packages"
  "compiled 12 packages"
  "3 files changed, 47 insertions(+), 12 deletions(-)"
  "installed 5 dependencies"
  Count real numbers from the output. Include test count, package count, or other concrete metrics. ONE line only.
- If exit code is non-zero (failure): you MUST include every error message, failed test name, file path, and line number — these are essential for debugging
- Strip: progress bars, timing info, download logs, PASS test output, "=== RUN" lines for passing tests
- Keep EXACTLY: failed test names, assertion errors, compiler errors, stack traces, file:line references
- Do NOT add symbols like ✔ or ✖ — just the text
- Do NOT add your own commentary — output ONLY the filtered result
- Do NOT wrap in markdown code blocks`

const imageSystemPrompt = `You are a vision preprocessor for AI coding agents. You receive images (screenshots, diagrams, UI mockups, terminal output, etc.) and produce a comprehensive text representation so the downstream agent never needs to see the raw pixels.

Your output MUST include:
1. **OCR**: All visible text, preserving layout, indentation, and hierarchy. Use monospace formatting for code/terminal content.
2. **Visual context**: What the image shows (app window, terminal, browser, diagram, mockup, error dialog, etc.), colors, layout, and spatial relationships.
3. **Actionable details**: Error messages, file paths, line numbers, URLs, button labels, form fields, status indicators — anything a coding agent would need to act on.

Rules:
- Be thorough — the agent CANNOT see the image, only your description
- Preserve exact text (don't paraphrase error messages or code)
- For terminal/code screenshots: reproduce the full text content
- For UI screenshots: describe the layout and all interactive elements
- For diagrams: describe the structure, relationships, and all labels
- Do NOT wrap in markdown code blocks unless reproducing code content
- Do NOT add meta-commentary like "this image shows" — just describe directly`

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
	if cfg.Provider == "fastvlm" {
		return nil, fmt.Errorf("fastvlm provider only supports image filtering (use 'tune image')")
	}
	return filterOpenAICompat(ctx, cfg, apiKey, userMsg, nil)
}

// FilterImage sends an image to the configured vision-capable LLM with resolution backoff.
// It OCRs the full-res image first for text reference, then sends the lowest resolution
// image that produces a good response — escalating only if needed.
func FilterImage(ctx context.Context, cfg *config.Config, imageData []byte, mimeType string, intent string) (*Result, error) {
	apiKey := cfg.ActiveAPIKey()
	if apiKey == "" && cfg.Provider != "ollama" && cfg.Provider != "fastvlm" {
		return nil, fmt.Errorf("no API key for provider %q", cfg.Provider)
	}

	if intent == "" {
		intent = "extract essential information from this image"
	}

	// OCR the full-res image first — free text extraction the model can use
	// as reference if characters are hard to read at low resolution
	ocrText := ocrImage(imageData)

	// FastVLM: local binary, processes image file directly — no base64, no HTTP
	if cfg.Provider == "fastvlm" {
		return filterFastVLMImage(ctx, imageData, intent)
	}

	// For Ollama: skip resolution backoff (too slow for multiple attempts),
	// send at a single reasonable resolution
	if cfg.Provider == "ollama" {
		resized, err := resizeImage(imageData, mimeType, 512)
		if err != nil {
			resized = imageData
		}
		fullIntent := intent
		if ocrText != "" {
			fullIntent = fmt.Sprintf("%s\n\nOCR text extracted from this image (use as reference for any hard-to-read characters):\n%s", intent, ocrText)
		}
		return filterOllamaImage(ctx, cfg, resized, fullIntent)
	}

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
				"content": fmt.Sprintf("/no_think\nIntent: %s", intent),
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

// ---------------------------------------------------------------------------
// FastVLM (local binary — Apple Silicon Neural Engine)
// ---------------------------------------------------------------------------

func fastvlmBinary() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "bin", "fastvlm-cli")
}

func filterFastVLMImage(ctx context.Context, imageData []byte, intent string) (*Result, error) {
	bin := fastvlmBinary()
	if _, err := os.Stat(bin); err != nil {
		return nil, fmt.Errorf("fastvlm-cli not found at %s (build with: cd ~/fastvlm-cli && swift build -c release)", bin)
	}

	// Write image to temp file (fastvlm-cli takes a file path, not stdin)
	tmpFile, err := os.CreateTemp("", "tune-fastvlm-*.png")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(imageData); err != nil {
		tmpFile.Close()
		return nil, fmt.Errorf("failed to write temp file: %w", err)
	}
	tmpFile.Close()

	home, _ := os.UserHomeDir()
	modelPath := filepath.Join(home, "ml-fastvlm/app/FastVLM/model")

	prompt := "Describe this image comprehensively for a coding AI agent that cannot see it. Include: 1) Full OCR of all visible text, preserving layout and indentation 2) Visual context: what the image shows (terminal, browser, UI, diagram, etc), layout, colors 3) Actionable details: error messages, file paths, line numbers, URLs, button labels, status indicators. Be thorough and preserve exact text."
	if intent != "" {
		prompt = intent
	}

	args := []string{
		tmpFile.Name(),
		"--prompt", prompt,
		"--model-path", modelPath,
		"--max-tokens", "240",
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	start := time.Now()
	output, err := cmd.Output()
	filterTime := time.Since(start)

	if err != nil {
		return nil, fmt.Errorf("fastvlm-cli failed: %w", err)
	}

	filtered := string(output)
	return &Result{
		Filtered:   filtered,
		FilterLen:  len(filtered),
		FilterTime: filterTime,
	}, nil
}
