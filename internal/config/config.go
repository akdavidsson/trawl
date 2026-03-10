package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	defaultAnthropicModel = "claude-sonnet-4-6"
	defaultGeminiModel    = "gemini-3.1-flash-lite-preview"
)

// Config holds runtime configuration for trawl.
type Config struct {
	APIKey   string `yaml:"api_key"`
	Model    string `yaml:"model"`
	Provider string `yaml:"-"` // "gemini" or "anthropic"
	Verbose  bool   `yaml:"-"`
}

// Load reads configuration from the environment and optional config file.
func Load() (*Config, error) {
	cfg := &Config{}

	cfgPath := filepath.Join(os.Getenv("HOME"), ".trawl", "config.yaml")
	if data, err := os.ReadFile(cfgPath); err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parsing config file %s: %w", cfgPath, err)
		}
	}

	// Prioritize Gemini over Anthropic
	geminiKey := os.Getenv("GOOGLE_GEMINI_APIKEY")
	anthropicKey := os.Getenv("ANTHROPIC_API_KEY")

	if geminiKey != "" {
		cfg.APIKey = geminiKey
		cfg.Provider = "gemini"
	} else if anthropicKey != "" {
		cfg.APIKey = anthropicKey
		cfg.Provider = "anthropic"
	} else if cfg.APIKey != "" {
		// Fallback for API key from config file (assume anthropic for backward compatibility unless model suggests otherwise, but we'll assume anthropic if no env vars are set)
		cfg.Provider = "anthropic"
	}

	if cfg.Model == "" {
		if cfg.Provider == "gemini" {
			cfg.Model = defaultGeminiModel
		} else {
			cfg.Model = defaultAnthropicModel
		}
	}

	return cfg, nil
}

// Validate returns an error if the configuration is unusable.
func (c *Config) Validate() error {
	if c.APIKey == "" {
		return fmt.Errorf("neither GOOGLE_GEMINI_APIKEY nor ANTHROPIC_API_KEY is set; export one or add api_key to ~/.trawl/config.yaml")
	}
	return nil
}

// CacheDir returns the path to the strategy cache directory, creating it if needed.
func CacheDir() (string, error) {
	dir := filepath.Join(os.Getenv("HOME"), ".trawl", "strategies")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating cache dir: %w", err)
	}
	return dir, nil
}
