package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config holds the application configuration.
type Config struct {
	APIAddr   string          `json:"api_addr,omitempty"`
	SaveDir   string          `json:"save_dir,omitempty"`
	Runtime   RuntimeConfig   `json:"runtime,omitempty"`
	Proactive ProactiveConfig `json:"proactive,omitempty"`
}

// RuntimeConfig holds configuration for the built-in companion runtime.
type RuntimeConfig struct {
	Enabled    bool           `json:"enabled,omitempty"`
	Name       string         `json:"name,omitempty"`
	MaxHistory int            `json:"max_history,omitempty"`
	Provider   ProviderConfig `json:"provider,omitempty"`
	Persona    PersonaConfig  `json:"persona,omitempty"`
}

type ProactiveConfig struct {
	Enabled         bool   `json:"enabled,omitempty"`
	DefaultTimezone string `json:"default_timezone,omitempty"`
}

// ProviderConfig holds direct model API settings for the packaged runtime.
type ProviderConfig struct {
	Type      string            `json:"type,omitempty"`
	Endpoint  string            `json:"endpoint,omitempty"`
	APIKey    string            `json:"api_key,omitempty"`
	Model     string            `json:"model,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	TimeoutMs int               `json:"timeout_ms,omitempty"`
}

// PersonaConfig holds the companion persona fields for the built-in runtime.
type PersonaConfig struct {
	SystemPrompt string `json:"system_prompt,omitempty"`
	Identity     string `json:"identity,omitempty"`
	Tone         string `json:"tone,omitempty"`
	Style        string `json:"style,omitempty"`
}

// DefaultConfig returns the default runtime-only configuration.
func DefaultConfig() *Config {
	return &Config{
		Runtime: RuntimeConfig{
			Enabled: true,
			Name:    "companion",
			Provider: ProviderConfig{
				Type:      "openai",
				Model:     "gpt-4o-mini",
				TimeoutMs: 120000,
			},
			Persona: PersonaConfig{
				Identity: "陪伴助手",
				Tone:     "温柔、自然、真诚",
				Style:    "简洁但有陪伴感",
			},
		},
		Proactive: ProactiveConfig{
			Enabled: true,
		},
	}
}

func preferredStateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".weone"), nil
}

// StateDir returns the runtime state directory.
func StateDir() (string, error) {
	if v := os.Getenv("WEONE_HOME"); v != "" {
		return filepath.Clean(v), nil
	}
	return preferredStateDir()
}

// ConfigPath returns the path to the config file.
func ConfigPath() (string, error) {
	root, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "config.json"), nil
}

// Load loads configuration from disk and environment variables.
func Load() (*Config, error) {
	cfg := DefaultConfig()

	path, err := ConfigPath()
	if err != nil {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			loadEnv(cfg)
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	loadEnv(cfg)
	return cfg, nil
}

func envValue(keys ...string) string {
	for _, key := range keys {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	return ""
}

func loadEnv(cfg *Config) {
	if v := envValue("WEONE_API_ADDR"); v != "" {
		cfg.APIAddr = v
	}
	if v := envValue("WEONE_SAVE_DIR"); v != "" {
		cfg.SaveDir = v
	}
	if v := envValue("WEONE_RUNTIME_ENABLED"); v != "" {
		cfg.Runtime.Enabled = v != "0" && v != "false"
	}
	if v := envValue("WEONE_RUNTIME_NAME"); v != "" {
		cfg.Runtime.Name = v
	}
	if v := envValue("WEONE_PROVIDER_TYPE"); v != "" {
		cfg.Runtime.Provider.Type = v
	}
	if v := envValue("WEONE_PROVIDER_ENDPOINT"); v != "" {
		cfg.Runtime.Provider.Endpoint = v
	}
	if v := envValue("WEONE_PROVIDER_API_KEY"); v != "" {
		cfg.Runtime.Provider.APIKey = v
	}
	if v := envValue("WEONE_PROVIDER_MODEL"); v != "" {
		cfg.Runtime.Provider.Model = v
	}
	if v := envValue("WEONE_PERSONA_SYSTEM_PROMPT"); v != "" {
		cfg.Runtime.Persona.SystemPrompt = v
	}
	if v := envValue("WEONE_PERSONA_IDENTITY"); v != "" {
		cfg.Runtime.Persona.Identity = v
	}
	if v := envValue("WEONE_PERSONA_TONE"); v != "" {
		cfg.Runtime.Persona.Tone = v
	}
	if v := envValue("WEONE_PERSONA_STYLE"); v != "" {
		cfg.Runtime.Persona.Style = v
	}
	if v := envValue("WEONE_PROACTIVE_ENABLED"); v != "" {
		cfg.Proactive.Enabled = v != "0" && v != "false"
	}
	if v := envValue("WEONE_PROACTIVE_DEFAULT_TIMEZONE"); v != "" {
		cfg.Proactive.DefaultTimezone = v
	}
}

// Save saves the configuration to disk.
func Save(cfg *Config) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	return os.WriteFile(path, data, 0o600)
}
