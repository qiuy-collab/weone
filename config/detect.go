package config

import (
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// agentCandidate defines one way to run an agent.
// Multiple candidates can map to the same agent name; the first detected wins.
type agentCandidate struct {
	Name      string   // agent name (e.g. "claude", "codex")
	Binary    string   // binary to look up in PATH
	Args      []string // extra args (e.g. ["acp"] for cursor)
	CheckArgs []string // optional capability probe args (must exit 0)
	Type      string   // "acp", "cli"
	Model     string   // default model
}

// agentCandidates is ordered by priority: for each agent name, earlier entries
// are preferred. E.g. claude ACP is tried before claude CLI.
var agentCandidates = []agentCandidate{
	// claude: prefer ACP, fallback to CLI
	{Name: "claude", Binary: "claude-agent-acp", Type: "acp", Model: "sonnet"},
	{Name: "claude", Binary: "claude", Type: "cli", Model: "sonnet"},
	// codex: prefer ACP, fallback to CLI
	{Name: "codex", Binary: "codex-acp", Type: "acp", Model: ""},
	{Name: "codex", Binary: "codex", Args: []string{"app-server", "--listen", "stdio://"}, CheckArgs: []string{"app-server", "--help"}, Type: "acp", Model: ""},
	{Name: "codex", Binary: "codex", Type: "cli", Model: ""},
	// ACP-only agents
	{Name: "cursor", Binary: "agent", Args: []string{"acp"}, Type: "acp", Model: ""},
	{Name: "kimi", Binary: "kimi", Args: []string{"acp"}, Type: "acp", Model: ""},
	{Name: "gemini", Binary: "gemini", Args: []string{"--acp"}, Type: "acp", Model: ""},
	{Name: "opencode", Binary: "opencode", Args: []string{"acp"}, Type: "acp", Model: ""},
	{Name: "pi", Binary: "pi-acp", Type: "acp", Model: ""},
	{Name: "copilot", Binary: "copilot", Args: []string{"--acp", "--stdio"}, Type: "acp", Model: ""},
	{Name: "droid", Binary: "droid", Args: []string{"exec", "--output-format", "acp"}, Type: "acp", Model: ""},
	{Name: "iflow", Binary: "iflow", Args: []string{"--experimental-acp"}, Type: "acp", Model: ""},
	{Name: "kiro", Binary: "kiro-cli", Args: []string{"acp"}, Type: "acp", Model: ""},
	{Name: "qwen", Binary: "qwen", Args: []string{"--acp"}, Type: "acp", Model: ""},
}

// defaultOrder defines the priority for choosing the default agent.
// Lower index = higher priority.
var defaultOrder = []string{
	"claude", "codex", "cursor", "kimi", "gemini", "opencode",
	"pi", "copilot", "droid", "iflow", "kiro", "qwen",
}

// DetectAndConfigure auto-detects local agents and populates the config.
// For each agent name, it picks the highest-priority candidate (acp > cli).
// Returns true if the config was modified.
func DetectAndConfigure(cfg *Config) bool {
	modified := false

	for _, candidate := range agentCandidates {
		if _, exists := cfg.Agents[candidate.Name]; exists {
			continue
		}

		path, err := lookPath(candidate.Binary)
		if err != nil {
			continue
		}

		if len(candidate.CheckArgs) > 0 && !commandProbe(path, candidate.CheckArgs) {
			log.Printf("[config] skipping %s at %s (type=%s): probe failed (%v)", candidate.Name, path, candidate.Type, candidate.CheckArgs)
			continue
		}

		log.Printf("[config] auto-detected %s at %s (type=%s)", candidate.Name, path, candidate.Type)
		cfg.Agents[candidate.Name] = AgentConfig{
			Type:    candidate.Type,
			Command: path,
			Args:    candidate.Args,
			Model:   candidate.Model,
		}
		modified = true
	}

	if cfg.Runtime.Enabled {
		runtimeName := cfg.Runtime.Name
		if runtimeName == "" {
			runtimeName = "companion"
		}
		if cfg.DefaultAgent != runtimeName {
			log.Printf("[config] setting default agent to packaged runtime: %s", runtimeName)
			cfg.DefaultAgent = runtimeName
			modified = true
		}
	} else if cfg.DefaultAgent == "" || !agentExists(cfg, cfg.DefaultAgent) {
		for _, name := range defaultOrder {
			if _, ok := cfg.Agents[name]; ok {
				if cfg.DefaultAgent != name {
					log.Printf("[config] setting default agent: %s", name)
					cfg.DefaultAgent = name
					modified = true
				}
				break
			}
		}
	}

	return modified
}

// commandProbe runs a binary with args and returns true if it exits 0.
func commandProbe(binary string, args []string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

func agentExists(cfg *Config, name string) bool {
	_, ok := cfg.Agents[name]
	return ok
}

// lookPath finds a binary by name. It first tries exec.LookPath (fast, uses
// current PATH). If that fails, it falls back to resolving via a login shell
// which sources the user's profile (~/.zshrc, ~/.bashrc) — this picks up
// binaries installed through version managers like nvm, mise, etc. that only
// add their paths in interactive shells.
func lookPath(binary string) (string, error) {
	if p, err := exec.LookPath(binary); err == nil {
		return p, nil
	}

	shell := "zsh"
	if runtime.GOOS != "darwin" {
		shell = "bash"
	}
	out, err := exec.Command(shell, "-lic", "which "+binary).Output()
	if err != nil {
		return "", fmt.Errorf("not found: %s", binary)
	}
	p := strings.TrimSpace(string(out))
	if p == "" || strings.Contains(p, "not found") {
		return "", fmt.Errorf("not found: %s", binary)
	}
	log.Printf("[config] resolved %s via login shell: %s", binary, p)
	return p, nil
}
