package config

import "testing"

func TestDefaultConfigHasRuntimeDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Runtime.Enabled {
		t.Fatal("Runtime.Enabled = false, want true")
	}
	if cfg.Runtime.Name != "companion" {
		t.Fatalf("Runtime.Name = %q, want %q", cfg.Runtime.Name, "companion")
	}
	if cfg.Runtime.Provider.Type != "openai" {
		t.Fatalf("Provider.Type = %q, want %q", cfg.Runtime.Provider.Type, "openai")
	}
}

func TestLoadEnvOverridesRuntimeFields(t *testing.T) {
	t.Setenv("WEONE_API_ADDR", "127.0.0.1:18011")
	t.Setenv("WEONE_PROVIDER_ENDPOINT", "https://api.example.com/v1/chat/completions")
	t.Setenv("WEONE_PROVIDER_MODEL", "gpt-5.4")

	cfg := DefaultConfig()
	loadEnv(cfg)

	if cfg.APIAddr != "127.0.0.1:18011" {
		t.Fatalf("APIAddr = %q, want %q", cfg.APIAddr, "127.0.0.1:18011")
	}
	if cfg.Runtime.Provider.Endpoint != "https://api.example.com/v1/chat/completions" {
		t.Fatalf("Provider.Endpoint = %q", cfg.Runtime.Provider.Endpoint)
	}
	if cfg.Runtime.Provider.Model != "gpt-5.4" {
		t.Fatalf("Provider.Model = %q, want %q", cfg.Runtime.Provider.Model, "gpt-5.4")
	}
}
