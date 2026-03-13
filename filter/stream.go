package filter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/partitura-ai/tune/config"
)

// FilterStream sends command output to the LLM and streams filtered tokens to w as they arrive.
func FilterStream(ctx context.Context, cfg *config.Config, w io.Writer, rawOutput string, exitCode int, intent string) (*Result, error) {
	apiKey := cfg.ActiveAPIKey()
	if apiKey == "" && cfg.Provider != "ollama" {
		return nil, fmt.Errorf("no API key for provider %q", cfg.Provider)
	}

	truncated := rawOutput
	if len(truncated) > cfg.MaxInput {
		truncated = truncated[:cfg.MaxInput]
	}

	userMsg := fmt.Sprintf("Intent: %s\nExit code: %d\nRaw output (%d chars):\n\n%s",
		intent, exitCode, len(rawOutput), truncated)

	var filtered string
	var filterTime time.Duration
	var err error

	start := time.Now()
	switch cfg.Provider {
	case "anthropic":
		filtered, err = streamChunkAnthropic(ctx, cfg, apiKey, systemPrompt, userMsg, w)
	case "ollama":
		filtered, err = streamChunkOllama(ctx, cfg, systemPrompt, userMsg, w)
	default:
		filtered, err = streamChunkOpenAI(ctx, cfg, apiKey, systemPrompt, userMsg, w)
	}
	filterTime = time.Since(start)

	if err != nil {
		return nil, err
	}

	return &Result{
		Filtered:   filtered,
		RawLen:     len(rawOutput),
		FilterLen:  len(filtered),
		FilterTime: filterTime,
	}, nil
}

// ---------------------------------------------------------------------------
// Shared SSE stream helpers (used by both FilterStream and FilterChunked)
// ---------------------------------------------------------------------------

// deltaParser extracts a content token from a single SSE JSON data line.
type deltaParser func(data []byte) string

func parseOpenAIDelta(data []byte) string {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal(data, &chunk) != nil || len(chunk.Choices) == 0 {
		return ""
	}
	return chunk.Choices[0].Delta.Content
}

func parseAnthropicDelta(data []byte) string {
	var event struct {
		Type  string `json:"type"`
		Delta struct {
			Text string `json:"text"`
		} `json:"delta"`
	}
	if json.Unmarshal(data, &event) != nil {
		return ""
	}
	if event.Type == "content_block_delta" {
		return event.Delta.Text
	}
	return ""
}

// doSSEStream makes a streaming request and writes tokens to w as they arrive.
// Returns the full concatenated response.
func doSSEStream(ctx context.Context, url, authValue, authScheme string, body map[string]any, w io.Writer, parse deltaParser) (string, error) {
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyJSON))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	// Auth header differs by provider
	if authScheme == "x-api-key" {
		req.Header.Set("x-api-key", authValue)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+authValue)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	var full strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		token := parse([]byte(data))
		if token != "" {
			full.WriteString(token)
			fmt.Fprint(w, token)
		}
	}

	return full.String(), nil
}

// doOllamaStream makes a streaming request to Ollama and writes tokens to w.
func doOllamaStream(ctx context.Context, body map[string]any, w io.Writer) (string, error) {
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "http://localhost:11434/api/chat", bytes.NewReader(bodyJSON))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("Ollama request failed (is Ollama running?): %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("Ollama error %d: %s", resp.StatusCode, string(respBody))
	}

	var full strings.Builder
	decoder := json.NewDecoder(resp.Body)
	for decoder.More() {
		var chunk struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Done bool `json:"done"`
		}
		if err := decoder.Decode(&chunk); err != nil {
			break
		}
		if chunk.Message.Content != "" {
			full.WriteString(chunk.Message.Content)
			fmt.Fprint(w, chunk.Message.Content)
		}
		if chunk.Done {
			break
		}
	}

	return full.String(), nil
}
