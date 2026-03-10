package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigLoadPrioritization(t *testing.T) {
	// Save existing env vars to restore them later
	origGemini := os.Getenv("GOOGLE_GEMINI_APIKEY")
	origAnthropic := os.Getenv("ANTHROPIC_API_KEY")
	origHome := os.Getenv("HOME")
	defer func() {
		os.Setenv("GOOGLE_GEMINI_APIKEY", origGemini)
		os.Setenv("ANTHROPIC_API_KEY", origAnthropic)
		os.Setenv("HOME", origHome)
	}()

	// Setup a fake HOME to prevent loading global config file
	fakeHome := t.TempDir()
	os.Setenv("HOME", fakeHome)
	// Create an empty config.yaml to avoid read errors if needed, though Load handles missing file
	os.MkdirAll(filepath.Join(fakeHome, ".trawl"), 0755)

	tests := []struct {
		name         string
		geminiKey    string
		anthropicKey string
		wantProvider string
		wantAPIKey   string
		wantModel    string
	}{
		{
			name:         "Both keys set prioritizes Gemini",
			geminiKey:    "gemini-key",
			anthropicKey: "anthropic-key",
			wantProvider: "gemini",
			wantAPIKey:   "gemini-key",
			wantModel:    defaultGeminiModel,
		},
		{
			name:         "Only Gemini key set",
			geminiKey:    "gemini-only-key",
			anthropicKey: "",
			wantProvider: "gemini",
			wantAPIKey:   "gemini-only-key",
			wantModel:    defaultGeminiModel,
		},
		{
			name:         "Only Anthropic key set",
			geminiKey:    "",
			anthropicKey: "anthropic-only-key",
			wantProvider: "anthropic",
			wantAPIKey:   "anthropic-only-key",
			wantModel:    defaultAnthropicModel,
		},
		{
			name:         "Neither key set",
			geminiKey:    "",
			anthropicKey: "",
			wantProvider: "",
			wantAPIKey:   "",
			wantModel:    defaultAnthropicModel, // default fallback
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("GOOGLE_GEMINI_APIKEY", tt.geminiKey)
			os.Setenv("ANTHROPIC_API_KEY", tt.anthropicKey)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}

			if cfg.Provider != tt.wantProvider {
				t.Errorf("Provider = %q, want %q", cfg.Provider, tt.wantProvider)
			}
			if cfg.APIKey != tt.wantAPIKey {
				t.Errorf("APIKey = %q, want %q", cfg.APIKey, tt.wantAPIKey)
			}
			if cfg.Model != tt.wantModel {
				t.Errorf("Model = %q, want %q", cfg.Model, tt.wantModel)
			}
		})
	}
}
