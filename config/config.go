package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/partitura-ai/tune/ui"
)

type Config struct {
	Provider           string            `json:"provider"`
	Model              string            `json:"model"`
	OpenRouterProvider string            `json:"openrouter_provider,omitempty"`
	APIKeys            map[string]string `json:"api_keys"`
	MaxInput           int               `json:"max_input"`
	Stream             bool              `json:"stream"`
}

func DefaultConfig() Config {
	return Config{
		Provider:           "openrouter",
		Model:              "openai/gpt-oss-20b",
		OpenRouterProvider: "groq",
		APIKeys:            map[string]string{},
		MaxInput:           16000,
		Stream:             true,
	}
}

func tuneDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tune")
}

func configPath() string {
	return filepath.Join(tuneDir(), "config.json")
}

func Load() (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(configPath())
	if err != nil {
		if os.IsNotExist(err) {
			// No config file — fill from env vars
			cfg.loadEnvKeys()
			return &cfg, nil
		}
		return nil, err
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		cfg = DefaultConfig()
	}
	if cfg.APIKeys == nil {
		cfg.APIKeys = map[string]string{}
	}
	if cfg.MaxInput == 0 {
		cfg.MaxInput = 16000
	}

	// Env vars fill in missing keys (config file takes precedence)
	cfg.loadEnvKeys()

	return &cfg, nil
}

func (c *Config) loadEnvKeys() {
	envMap := map[string]string{
		"openrouter": "OPENROUTER_API_KEY",
		"openai":     "OPENAI_API_KEY",
		"anthropic":  "ANTHROPIC_API_KEY",
		"gemini":     "GEMINI_API_KEY",
	}
	// TUNE_API_KEY is a universal fallback for the active provider
	tuneKey := os.Getenv("TUNE_API_KEY")
	for provider, envVar := range envMap {
		if c.APIKeys[provider] == "" {
			if v := os.Getenv(envVar); v != "" {
				c.APIKeys[provider] = v
			}
		}
	}

	// TUNE_API_KEY fills the active provider if still empty
	if tuneKey != "" && c.APIKeys[c.Provider] == "" {
		c.APIKeys[c.Provider] = tuneKey
	}
}

func (c *Config) Save() error {
	if err := os.MkdirAll(tuneDir(), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(), data, 0644)
}

// ActiveAPIKey returns the API key for the current provider.
func (c *Config) ActiveAPIKey() string {
	return c.APIKeys[c.Provider]
}

// Print displays the current configuration.
func (c *Config) Print() {
	authStatus := ""
	if c.Provider == "ollama" {
		authStatus = ui.Success.Render("local (no key needed)")
	} else if key := c.APIKeys[c.Provider]; key != "" {
		authStatus = ui.Success.Render(maskKey(key))
	} else {
		authStatus = ui.Warning.Render("(no key — run 'tune config apikey <key>')")
	}

	content := fmt.Sprintf(
		"%s  %s\n%s  %s\n%s  %s\n%s  %s\n\n%s  %s",
		ui.Label.Render("Provider: "), ui.Value.Render(c.Provider),
		ui.Label.Render("Model:    "), ui.Value.Render(c.Model),
		ui.Label.Render("Stream:   "), ui.Value.Render(fmt.Sprintf("%v", c.Stream)),
		ui.Label.Render("Max input:"), ui.Value.Render(fmt.Sprintf("%d chars", c.MaxInput)),
		ui.Label.Render("Auth:     "), authStatus,
	)

	fmt.Println(ui.Title.Render("🎼 Tune Configuration"))
	fmt.Println(ui.Box.Render(content))
	fmt.Println(ui.Faint.Render("  " + configPath()))
}

func maskKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

// Providers returns valid provider names.
func Providers() []string {
	return []string{"openrouter", "ollama", "openai", "anthropic", "gemini"}
}

// ValidProvider checks if a provider name is valid.
func ValidProvider(p string) bool {
	for _, v := range Providers() {
		if v == p {
			return true
		}
	}
	return false
}
