package cmd

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/qiuy-collab/weone/agent"
	"github.com/qiuy-collab/weone/api"
	"github.com/qiuy-collab/weone/config"
	"github.com/qiuy-collab/weone/ilink"
	internalruntime "github.com/qiuy-collab/weone/internal/runtime"
	"github.com/qiuy-collab/weone/materials"
	"github.com/qiuy-collab/weone/memory"
	"github.com/qiuy-collab/weone/messaging"
	"github.com/qiuy-collab/weone/proactive"
	"github.com/spf13/cobra"
)

const defaultAPIAddr = "127.0.0.1:18011"

var (
	foregroundFlag bool
	apiAddrFlag    string
)

func init() {
	startCmd.Flags().BoolVarP(&foregroundFlag, "foreground", "f", false, "Run in foreground (default is background)")
	startCmd.Flags().StringVar(&apiAddrFlag, "api-addr", "", "API server listen address (default 127.0.0.1:18011)")
	rootCmd.AddCommand(startCmd)
}

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the WeChat message bridge (auto-login if needed)",
	RunE:  runStart,
}

func runStart(cmd *cobra.Command, args []string) error {
	apiAddr := resolveAPIAddr("")
	if !foregroundFlag {
		if running, pid := currentInstancePID(); running {
			fmt.Printf("weone is already running (pid=%d)\n", pid)
			fmt.Printf("Control panel: http://%s\n", apiAddr)
			return nil
		}
		if reachable, err := apiServerReachable(apiAddr); err == nil && reachable {
			return fmt.Errorf("weone appears to already be running at http://%s, but the pid file is missing; stop the existing process first", apiAddr)
		}
		// Start the web service even with zero accounts; users can bind later from the control panel.
		return runDaemon(apiAddr)
	}

	if running, pid := currentInstancePID(); running {
		return fmt.Errorf("weone is already running (pid=%d), open http://%s or run `weone stop` first", pid, apiAddr)
	}
	if reachable, err := apiServerReachable(apiAddr); err == nil && reachable {
		return fmt.Errorf("weone appears to already be running at http://%s, but the pid file is missing; stop the existing process first", apiAddr)
	}
	if err := writePIDFile(os.Getpid()); err != nil {
		return fmt.Errorf("write pid file: %w", err)
	}
	defer cleanupPIDFile(os.Getpid())

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Load all accounts
	accounts, err := ilink.LoadAllCredentials()
	if err != nil {
		return fmt.Errorf("failed to load credentials: %w", err)
	}

	// Load config
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	log.Printf("[start] loaded config runtime_enabled=%v runtime_name=%q", cfg.Runtime.Enabled, cfg.Runtime.Name)

	// Create handler with an agent factory for on-demand agent creation
	runtimeName := cfg.Runtime.Name
	if runtimeName == "" {
		runtimeName = "companion"
	}
	cfg.Runtime.Name = runtimeName
	cfg.Runtime.Enabled = true
	cfg.DefaultAgent = runtimeName
	cfg.Agents = map[string]config.AgentConfig{}

	var runtimeSvc *internalruntime.Service
	if cfg.Runtime.Enabled {
		runtimeSvc = internalruntime.NewService(cfg.Runtime)
	}
	importAnalyzer := materials.NewImportAnalyzer(cfg.Runtime.Provider)

	memorySvc, err := memory.NewService()
	if err != nil {
		return fmt.Errorf("init memory service: %w", err)
	}
	defer memorySvc.Close()

	materialsSvc := materials.NewService(importAnalyzer)
	accountManager := api.NewAccountRuntimeManager()
	proactiveSvc := proactive.NewService(runtimeSvc, memorySvc, materialsSvc, accountManager)
	proactiveScheduler := proactive.NewScheduler(proactiveSvc)
	proactiveSvc.SetScheduler(proactiveScheduler)

	handler := messaging.NewHandler(
		nil,
		func(name string) error {
			cfg.DefaultAgent = runtimeName
			return config.Save(cfg)
		},
		runtimeName,
		runtimeSvc,
		memorySvc,
		materialsSvc,
	)

	// Populate agent metas for /status
	var metas []messaging.AgentMeta
	metas = append(metas, messaging.AgentMeta{
		Name:    runtimeName,
		Type:    "runtime",
		Command: cfg.Runtime.Provider.Endpoint,
		Model:   cfg.Runtime.Provider.Model,
	})
	workDirs := make(map[string]string)
	handler.SetAgentMetas(metas)
	handler.SetAgentWorkDirs(workDirs)

	// Runtime-only mode: disable custom aliases for external agents
	handler.SetCustomAliases(nil)
	handler.SetInboundRecorder(func(botID, userID string, at time.Time) {
		if err := proactiveSvc.RecordInbound(botID, userID, at); err != nil {
			log.Printf("[proactive] record inbound failed bot=%s user=%s err=%v", botID, userID, err)
		}
	})
	handler.SetRuntimeTurnRecorder(func(ctx context.Context, botID, userID, message, reply, memoryContext string) {
		proactiveSvc.ProcessConversationDecision(ctx, botID, userID, message, reply, memoryContext)
	})

	// Set save directory for images/files if configured
	if cfg.SaveDir != "" {
		handler.SetSaveDir(cfg.SaveDir)
		log.Printf("Image save directory: %s", cfg.SaveDir)
	}

	// Start default reply engine initialization in background so monitors can start immediately
	go func() {
		if cfg.Runtime.Enabled {
			cfg.DefaultAgent = runtimeName
			log.Printf("Using packaged runtime %q as default", runtimeName)
			return
		}

		if cfg.DefaultAgent == "" {
			log.Println("No default agent configured, staying in echo mode")
			return
		}

		log.Printf("Initializing default agent %q in background...", cfg.DefaultAgent)
		ag := createAgentByName(ctx, cfg, cfg.DefaultAgent)
		if ag == nil {
			log.Printf("Failed to initialize default agent %q, staying in echo mode", cfg.DefaultAgent)
			return
		}
		handler.SetDefaultAgent(cfg.DefaultAgent, ag)
	}()

	// Start HTTP API server for sending messages
	accountManager.SetMonitorStarter(func(creds *ilink.Credentials) {
		if creds == nil {
			return
		}
		client := ilink.NewClient(creds)
		accountCtx, accountCancel := context.WithCancel(ctx)
		accountManager.Add(client, accountCancel)
		log.Printf("[start] account bot_id=%s status=ready", client.BotID())
		go runMonitorWithRestart(accountCtx, creds, handler)
	})
	log.Printf("[start] prepared %d account client(s)", len(accounts))
	for _, creds := range accounts {
		accountManager.AddCredentials(creds)
	}
	// Resolve API addr: flag > env/config > default
	apiAddr = resolveAPIAddr(cfg.APIAddr)
	apiServer := api.NewServer(accountManager, handler, memorySvc, materialsSvc, proactiveSvc, apiAddr, cfg, config.Save, func() string {
		snapshot := handler.StatusSnapshot()
		if snapshot.DefaultAgent == "" {
			return ""
		}
		return snapshot.DefaultAgent
	})
	go func() {
		if err := apiServer.Run(ctx); err != nil {
			log.Printf("API server error: %v", err)
		}
	}()
	if cfg.Proactive.Enabled {
		if err := proactiveScheduler.Start(ctx); err != nil {
			return fmt.Errorf("start proactive scheduler: %w", err)
		}
		log.Printf("[proactive] scheduler started")
	}

	// Start monitors immediately — they will use runtime-only routing
	log.Printf("[start] starting message bridge account_count=%d api_addr=%s runtime=%s", len(accounts), apiAddr, runtimeName)

	<-ctx.Done()
	log.Println("All monitors stopped")
	return nil
}

// runMonitorWithRestart runs a monitor with automatic restart on failure.
func runMonitorWithRestart(ctx context.Context, creds *ilink.Credentials, handler *messaging.Handler) {
	const maxRestartDelay = 30 * time.Second
	restartDelay := 3 * time.Second

	for {
		log.Printf("[%s] Starting monitor...", creds.ILinkBotID)

		client := ilink.NewClient(creds)
		monitor, err := ilink.NewMonitor(client, handler.HandleMessage)
		if err != nil {
			log.Printf("[%s] Failed to create monitor: %v", creds.ILinkBotID, err)
		} else {
			err = monitor.Run(ctx)
		}

		// If context is cancelled, exit
		if ctx.Err() != nil {
			return
		}

		log.Printf("[%s] Monitor stopped: %v, restarting in %s", creds.ILinkBotID, err, restartDelay)
		select {
		case <-time.After(restartDelay):
		case <-ctx.Done():
			return
		}

		// Exponential backoff for restarts, capped
		restartDelay *= 2
		if restartDelay > maxRestartDelay {
			restartDelay = maxRestartDelay
		}
	}
}

// createAgentByName creates and starts an agent by its config name.
// Returns nil if the agent is not configured or fails to start.
func createAgentByName(ctx context.Context, cfg *config.Config, name string) agent.Agent {
	agCfg, ok := cfg.Agents[name]
	if !ok {
		log.Printf("[agent] %q not found in config", name)
		return nil
	}

	switch agCfg.Type {
	case "acp":
		ag := agent.NewACPAgent(agent.ACPAgentConfig{
			Command:      agCfg.Command,
			Args:         agCfg.Args,
			Cwd:          agCfg.Cwd,
			Env:          agCfg.Env,
			Model:        agCfg.Model,
			SystemPrompt: agCfg.SystemPrompt,
		})
		if err := ag.Start(ctx); err != nil {
			log.Printf("[agent] failed to start ACP agent %q: %v", name, err)
			return nil
		}
		log.Printf("[agent] started ACP agent: %s (command=%s, type=%s, model=%s)", name, agCfg.Command, agCfg.Type, agCfg.Model)
		return ag
	case "cli":
		ag := agent.NewCLIAgent(agent.CLIAgentConfig{
			Name:         name,
			Command:      agCfg.Command,
			Args:         agCfg.Args,
			Cwd:          agCfg.Cwd,
			Env:          agCfg.Env,
			Model:        agCfg.Model,
			SystemPrompt: agCfg.SystemPrompt,
		})
		log.Printf("[agent] created CLI agent: %s (command=%s, type=%s, model=%s)", name, agCfg.Command, agCfg.Type, agCfg.Model)
		return ag
	case "http":
		if agCfg.Endpoint == "" {
			log.Printf("[agent] HTTP agent %q has no endpoint", name)
			return nil
		}
		ag := agent.NewHTTPAgent(agent.HTTPAgentConfig{
			Endpoint:     agCfg.Endpoint,
			APIKey:       agCfg.APIKey,
			Headers:      agCfg.Headers,
			Model:        agCfg.Model,
			SystemPrompt: agCfg.SystemPrompt,
			MaxHistory:   agCfg.MaxHistory,
		})
		log.Printf("[agent] created HTTP agent: %s (endpoint=%s, model=%s)", name, agCfg.Endpoint, agCfg.Model)
		return ag
	default:
		log.Printf("[agent] unknown type %q for %q", agCfg.Type, name)
		return nil
	}
}

// doLogin runs the interactive QR login flow and returns credentials.
func doLogin(ctx context.Context) (*ilink.Credentials, error) {
	fmt.Println("Fetching QR code...")
	qr, err := ilink.FetchQRCode(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch QR code: %w", err)
	}

	fmt.Println("\nScan this QR code with WeChat:")
	fmt.Println()
	qrterminal.GenerateWithConfig(qr.QRCodeImgContent, qrterminal.Config{
		Level:          qrterminal.L,
		Writer:         os.Stdout,
		HalfBlocks:     true,
		BlackChar:      qrterminal.BLACK_BLACK,
		WhiteBlackChar: qrterminal.WHITE_BLACK,
		WhiteChar:      qrterminal.WHITE_WHITE,
		BlackWhiteChar: qrterminal.BLACK_WHITE,
		QuietZone:      1,
	})
	fmt.Printf("\nQR URL: %s\n", qr.QRCodeImgContent)
	fmt.Println("\nWaiting for scan...")

	lastStatus := ""
	creds, err := ilink.PollQRStatus(ctx, qr.QRCode, func(status string) {
		if status != lastStatus {
			lastStatus = status
			switch status {
			case "scaned":
				fmt.Println("QR code scanned! Please confirm on your phone.")
			case "confirmed":
				fmt.Println("Login confirmed!")
			case "expired":
				fmt.Println("QR code expired.")
			}
		}
	})
	if err != nil {
		return nil, err
	}

	if err := ilink.SaveCredentials(creds); err != nil {
		return nil, fmt.Errorf("failed to save credentials: %w", err)
	}

	dir, _ := ilink.CredentialsPath()
	fmt.Printf("\nLogin successful! Credentials saved to %s\n", dir)
	fmt.Printf("Bot ID: %s\n\n", creds.ILinkBotID)
	return creds, nil
}

// --- Daemon mode ---

func weoneDir() string {
	root, err := config.StateDir()
	if err == nil {
		return root
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".weone")
}

func stateFile(name string) string {
	return filepath.Join(weoneDir(), name)
}

func pidFile() string {
	return stateFile("weone.pid")
}

func preferredPIDFile() string {
	return stateFile("weone.pid")
}

func logFile() string {
	return stateFile("weone.log")
}

func preferredLogFile() string {
	return stateFile("weone.log")
}

// runDaemon spawns weone start (without --daemon) as a background process.
func runDaemon(apiAddr string) error {
	// Ensure log directory exists
	if err := os.MkdirAll(weoneDir(), 0o700); err != nil {
		return fmt.Errorf("create weone dir: %w", err)
	}

	// Open log file
	lf, err := os.OpenFile(preferredLogFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}

	// Re-exec ourselves without --daemon
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find executable: %w", err)
	}

	args := []string{"start", "-f"}
	if apiAddr != "" && apiAddr != defaultAPIAddr {
		args = append(args, "--api-addr", apiAddr)
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdout = lf
	cmd.Stderr = lf
	setSysProcAttr(cmd)

	if err := cmd.Start(); err != nil {
		lf.Close()
		return fmt.Errorf("start daemon: %w", err)
	}

	// Save PID
	pid := cmd.Process.Pid
	if err := writePIDFile(pid); err != nil {
		lf.Close()
		_ = cmd.Process.Kill()
		return fmt.Errorf("write pid file: %w", err)
	}

	// Detach — don't wait
	cmd.Process.Release()
	lf.Close()

	fmt.Printf("weone started in background (pid=%d)\n", pid)
	fmt.Printf("Control panel: http://%s\n", apiAddr)
	fmt.Printf("Log: %s\n", preferredLogFile())
	fmt.Printf("Stop: weone stop\n")
	return nil
}

func readPid() (int, error) {
	data, err := os.ReadFile(pidFile())
	if err != nil {
		return 0, err
	}
	var pid int
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil {
		return 0, err
	}
	return pid, nil
}

func writePIDFile(pid int) error {
	if err := os.MkdirAll(weoneDir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(pidFile(), []byte(fmt.Sprintf("%d", pid)), 0o644)
}

func cleanupPIDFile(pid int) {
	currentPID, err := readPid()
	if err != nil {
		return
	}
	if currentPID == pid {
		_ = os.Remove(pidFile())
	}
}

func currentInstancePID() (bool, int) {
	pid, err := readPid()
	if err != nil {
		return false, 0
	}
	if pid == os.Getpid() {
		return false, 0
	}
	if processExists(pid) {
		return true, pid
	}
	_ = os.Remove(pidFile())
	return false, 0
}

func resolveAPIAddr(configAddr string) string {
	if apiAddrFlag != "" {
		return apiAddrFlag
	}
	if configAddr != "" {
		return configAddr
	}
	return defaultAPIAddr
}

func apiServerReachable(addr string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/health", nil)
	if err != nil {
		return false, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK, nil
}

func processExists(pid int) bool {
	return processExistsPlatform(pid)
}

func stopProcess(pid int) bool {
	if !processExists(pid) {
		return false
	}
	_ = terminateProcess(pid, false)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !processExists(pid) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = terminateProcess(pid, true)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processExists(pid) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return !processExists(pid)
}

// stopAllWeclaw kills the running weclaw process tracked by the PID file.
func stopAllWeclaw() bool {
	pid, err := readPid()
	if err != nil {
		return false
	}
	if !processExists(pid) {
		_ = os.Remove(pidFile())
		return false
	}
	stopped := stopProcess(pid)
	_ = os.Remove(pidFile())
	return stopped
}
