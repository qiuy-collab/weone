package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// Config holds the application configuration.
type Config struct {
	DefaultAgent string                 `json:"default_agent"`
	APIAddr      string                 `json:"api_addr,omitempty"`
	SaveDir      string                 `json:"save_dir,omitempty"`
	Runtime      RuntimeConfig          `json:"runtime,omitempty"`
	Proactive    ProactiveConfig        `json:"proactive,omitempty"`
	Agents       map[string]AgentConfig `json:"agents"`
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

// PersonaConfig holds the phase-1 persona fields for the built-in runtime.
type PersonaConfig struct {
	SystemPrompt string `json:"system_prompt,omitempty"`
	Identity     string `json:"identity,omitempty"`
	Tone         string `json:"tone,omitempty"`
	Style        string `json:"style,omitempty"`
}

// AgentConfig holds configuration for a single agent.
type AgentConfig struct {
	Type         string            `json:"type"`                    // "acp", "cli", or "http"
	Command      string            `json:"command,omitempty"`       // binary path (cli/acp type)
	Args         []string          `json:"args,omitempty"`          // extra args for command (e.g. ["acp"] for cursor)
	Aliases      []string          `json:"aliases,omitempty"`       // custom trigger commands (e.g. ["gpt", "4o"])
	Cwd          string            `json:"cwd,omitempty"`           // working directory (workspace)
	Env          map[string]string `json:"env,omitempty"`           // extra environment variables (cli/acp type)
	Model        string            `json:"model,omitempty"`         // model name
	SystemPrompt string            `json:"system_prompt,omitempty"` // system prompt
	Endpoint     string            `json:"endpoint,omitempty"`      // API endpoint (http type)
	APIKey       string            `json:"api_key,omitempty"`       // API key (http type)
	Headers      map[string]string `json:"headers,omitempty"`       // extra HTTP headers (http type)
	MaxHistory   int               `json:"max_history,omitempty"`   // max history (http type)
}

// BuildAliasMap builds a map from custom alias to agent name from all agent configs.
// It logs warnings for conflicts: duplicate aliases and aliases shadowing agent keys.
func BuildAliasMap(agents map[string]AgentConfig) map[string]string {
	// Built-in commands that cannot be overridden
	reserved := map[string]bool{
		"info": true, "help": true, "new": true, "clear": true, "cwd": true,
	}

	m := make(map[string]string)
	for name, cfg := range agents {
		for _, alias := range cfg.Aliases {
			if reserved[alias] {
				log.Printf("[config] WARNING: alias %q for agent %q conflicts with built-in command, ignored", alias, name)
				continue
			}
			if existing, ok := m[alias]; ok {
				log.Printf("[config] WARNING: alias %q is defined by both %q and %q, using %q", alias, existing, name, name)
			}
			m[alias] = name
		}
	}

	// Warn if a custom alias shadows an agent key
	for alias, target := range m {
		if _, isAgent := agents[alias]; isAgent && alias != target {
			log.Printf("[config] WARNING: alias %q (-> %q) shadows agent key %q", alias, target, alias)
		}
	}

	return m
}

// DefaultConfig returns an empty configuration.
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
		Agents: make(map[string]AgentConfig),
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
	if cfg.Agents == nil {
		cfg.Agents = make(map[string]AgentConfig)
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
	if v := envValue("WEONE_DEFAULT_AGENT"); v != "" {
		cfg.DefaultAgent = v
	}
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
