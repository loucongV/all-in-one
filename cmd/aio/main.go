package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
	"github.com/loucongV/all-in-one/internal/adapter"
	"github.com/loucongV/all-in-one/internal/daemon"
	"github.com/loucongV/all-in-one/internal/protocol"
	"github.com/loucongV/all-in-one/internal/quota"
	"github.com/loucongV/all-in-one/internal/store"
	"github.com/loucongV/all-in-one/internal/tui"
	"github.com/loucongV/all-in-one/internal/version"
)

func main() {
	noMouse := false
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "daemon":
			runDaemon(os.Args[2:])
			return
		case "hook":
			runHook(os.Args[2:])
			return
		case "agents":
			runAgents()
			return
		case "status":
			runStatus()
			return
		case "quota":
			runQuota(os.Args[2:])
			return
		case "stop-daemon":
			runStopDaemon(os.Args[2:])
			return
		case "-v", "--version", "version":
			runVersion()
			return
		case "--no-mouse":
			noMouse = true
		case "-h", "--help", "help":
			printUsage()
			return
		}
	}

	runTUI(noMouse)
}

func printUsage() {
	fmt.Printf(`aio %s - All-in-One Multi-Agent CLI Orchestrator (Codex / Claude Code / Antigravity / Cursor)

Usage:
  aio                 Launch interactive TUI (auto-starts background daemon)
  aio --no-mouse      Launch TUI with native terminal mouse (native right-click paste over SSH)
  aio version         Print client & daemon version information (-v, --version)
  aio daemon          Run background daemon in foreground
  aio agents          List detected local Agent CLIs
  aio status          Show summary of running / waiting tasks and quotas
  aio quota [--probe] Show (and optionally live-probe) /usage & /status quotas
  aio hook            Report lifecycle status event to daemon (used by agent hooks)
  aio stop-daemon [-f] Gracefully stop the background daemon (prompts if runs are active)
`, version.Short())
}

func runVersion() {
	fmt.Println(version.Full())
	homeDir, err := store.HomeDir()
	if err != nil {
		return
	}
	client := daemon.NewClient(store.SocketPath(homeDir))
	info, err := client.HealthInfo()
	if err != nil {
		return
	}
	dv := info.Version
	if dv == "" {
		dv = "legacy"
	}
	fmt.Printf("daemon: %s (pid %d, active runs: %d)\n", dv, info.PID, info.ActiveRuns)
	if exe, err := os.Executable(); err == nil {
		if fp := daemon.BinFingerprint(exe); fp != "" && info.BinFingerprint != "" && info.BinFingerprint != fp {
			fmt.Println("note: 后台 daemon 为旧版本，空闲时可执行 `aio stop-daemon` 后重开升级")
		}
	}
}

func runDaemon(args []string) {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	sockFlag := fs.String("sock", "", "Unix socket path (default ~/.aio/daemon.sock)")
	_ = fs.Parse(args)

	homeDir, err := store.HomeDir()
	if err != nil {
		fatalf("resolve home dir: %v", err)
	}
	st, err := store.Open(homeDir)
	if err != nil {
		fatalf("open store: %v", err)
	}
	sockPath := *sockFlag
	if sockPath == "" {
		sockPath = store.SocketPath(homeDir)
	}

	srv := daemon.NewServer(st, adapter.DefaultRegistry(), sockPath, "")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := srv.Start(ctx); err != nil {
		fatalf("daemon exited with error: %v", err)
	}
}

func runHook(args []string) {
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	runID := fs.String("run", os.Getenv("AIO_RUN_ID"), "Run ID")
	event := fs.String("event", "done", "Event type: waiting|done|running|error")
	detail := fs.String("detail", "", "Optional status detail")
	agyState := fs.Bool("agy-state", false, "Read Antigravity title/statusLine JSON payload from stdin")
	sockFlag := fs.String("sock", os.Getenv("AIO_SOCK_PATH"), "Daemon unix socket path")
	_ = fs.Parse(args)

	ev := *event
	dt := *detail
	sessionID := ""
	shouldSend := true

	if *agyState {
		// Always emit a plain-text title so agy never flags the title command as failed.
		defer fmt.Println("agy")
		raw, err := io.ReadAll(io.LimitReader(os.Stdin, 256*1024))
		if err != nil || len(raw) == 0 {
			return
		}
		var payload struct {
			AgentState              string `json:"agent_state"`
			ConversationID          string `json:"conversation_id"`
			ToolConfirmationPending bool   `json:"tool_confirmation_pending"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return
		}
		sessionID = strings.TrimSpace(payload.ConversationID)
		ev, dt, shouldSend = mapAgyStateToHook(payload.AgentState, payload.ToolConfirmationPending)
	} else {
		// Extract session_id / thread-id & Claude notification_type from Codex notify trailing JSON or Claude stdin JSON.
		var notifyType string
		sessionID, notifyType = extractHookPayload(fs.Args())
		if ev == "waiting" && notifyType != "" {
			switch strings.ToLower(notifyType) {
			case "idle_prompt", "auth_success":
				// Claude Code fires Notification(idle_prompt) when merely sitting idle at prompt,
				// not when requesting tool approval — keep it in idle/done state.
				ev = "done"
				if dt == "" {
					dt = "claude:" + strings.ToLower(notifyType)
				}
			default:
				if dt == "" {
					dt = "claude:" + strings.ToLower(notifyType)
				}
			}
		}
	}

	if !shouldSend || *runID == "" {
		return
	}
	sock := *sockFlag
	if sock == "" {
		homeDir, err := store.HomeDir()
		if err != nil {
			return
		}
		sock = store.SocketPath(homeDir)
	}
	client := daemon.NewClient(sock)
	_ = client.SendHook(protocol.HookEventRequest{
		RunID:     *runID,
		Event:     ev,
		Detail:    dt,
		SessionID: sessionID,
	})
}

func extractHookPayload(extraArgs []string) (sessionID, notificationType string) {
	parseJSON := func(raw []byte) (string, string) {
		var p struct {
			SessionID        string `json:"session_id"`
			ThreadID         string `json:"thread-id"`
			ThreadIDSnake    string `json:"thread_id"`
			ConversationID   string `json:"conversation_id"`
			NotificationType string `json:"notification_type"`
		}
		if json.Unmarshal(raw, &p) != nil {
			return "", ""
		}
		sid := ""
		for _, cand := range []string{p.SessionID, p.ThreadID, p.ThreadIDSnake, p.ConversationID} {
			if s := strings.TrimSpace(cand); s != "" {
				sid = s
				break
			}
		}
		return sid, strings.TrimSpace(p.NotificationType)
	}
	// 1. Codex notify appends the JSON notification payload as a trailing positional argument.
	for i := len(extraArgs) - 1; i >= 0; i-- {
		a := strings.TrimSpace(extraArgs[i])
		if strings.HasPrefix(a, "{") {
			if sid, nt := parseJSON([]byte(a)); sid != "" || nt != "" {
				return sid, nt
			}
		}
	}
	// 2. Claude Code hooks pipe a JSON object containing `session_id` / `notification_type` on stdin.
	if fi, err := os.Stdin.Stat(); err == nil && (fi.Mode()&os.ModeCharDevice) == 0 {
		ch := make(chan []byte, 1)
		go func() {
			b, _ := io.ReadAll(io.LimitReader(os.Stdin, 64*1024))
			ch <- b
		}()
		select {
		case raw := <-ch:
			if len(raw) > 0 {
				return parseJSON(raw)
			}
		case <-time.After(120 * time.Millisecond):
		}
	}
	return "", ""
}

func mapAgyStateToHook(agentState string, toolConfirmationPending bool) (event, detail string, send bool) {
	if toolConfirmationPending {
		return "waiting", "agy:waiting_confirmation", true
	}
	switch strings.ToLower(strings.TrimSpace(agentState)) {
	case "working", "tool_use":
		return "running", "agy:" + strings.ToLower(strings.TrimSpace(agentState)), true
	case "reviewing":
		return "waiting", "agy:reviewing", true
	case "idle":
		return "agy_idle", "agy:idle", true
	case "error":
		return "error", "agy:error", true
	default:
		// Ignore transient startup states (authenticating / initializing / empty).
		return "", "", false
	}
}

func runAgents() {
	reg := adapter.DefaultRegistry()
	fmt.Println("Detected Agent CLIs:")
	for _, a := range reg.ListInfo() {
		if a.Available {
			fmt.Printf("  [OK]   %-12s (%s -> %s)\n", a.Label, a.Binary, a.Path)
		} else {
			fmt.Printf("  [MISS] %-12s (%s not found in PATH)\n", a.Label, a.Binary)
		}
	}
}

func runStatus() {
	homeDir, err := store.HomeDir()
	if err != nil {
		fatalf("%v", err)
	}
	client := daemon.NewClient(store.SocketPath(homeDir))
	if err := client.Health(); err != nil {
		fmt.Println("aio daemon is not running.")
		return
	}
	snap, err := client.State()
	if err != nil {
		fatalf("fetch state: %v", err)
	}
	verLine := "aio " + version.Short()
	if snap.DaemonVersion != "" && snap.DaemonVersion != version.Short() {
		verLine += " (daemon: " + snap.DaemonVersion + ")"
	}
	fmt.Printf("%s | Projects: %d | Tasks: %d | Runs: %d\n", verLine, len(snap.Projects), len(snap.Tasks), len(snap.Runs))
	for _, r := range snap.Runs {
		fmt.Printf("  %-8s %-14s %-28s %s\n", r.Agent, r.Status, r.Branch, r.StatusDetail)
	}
	if len(snap.Quotas) > 0 {
		fmt.Println("\nAgent Quotas:")
		for _, q := range snap.Quotas {
			fmt.Printf("  %-8s [%-7s] %s\n", q.Agent, q.State, q.Summary)
		}
	}
}

func runQuota(args []string) {
	fs := flag.NewFlagSet("quota", flag.ExitOnError)
	doProbe := fs.Bool("probe", false, "Run live silent /usage probe now")
	debugRaw := fs.Bool("debug", false, "Print raw probed /usage screen text")
	_ = fs.Parse(args)

	homeDir, err := store.HomeDir()
	if err != nil {
		fatalf("%v", err)
	}
	st, err := store.Open(homeDir)
	if err != nil {
		fatalf("%v", err)
	}
	c := quota.NewCollector(homeDir, st.Config().QuotaCommands, nil)
	if *doProbe {
		ctx := context.Background()
		for _, ag := range []string{"agy", "claude"} {
			screen, pErr := quota.ProbeAgentUsage(ctx, ag, homeDir)
			if *debugRaw {
				fmt.Printf("=== RAW %s (err=%v) ===\n%s\n", ag, pErr, screen)
			}
			if pErr == nil && screen != "" {
				c.IngestScreen(ag, screen)
			}
		}
	}
	for _, q := range c.Snapshot() {
		fmt.Printf("%-8s [%-7s] %s\n", q.Agent, q.State, q.Summary)
		if q.Detail != "" {
			fmt.Printf("  %s\n", q.Detail)
		}
	}
}

func runStopDaemon(args []string) {
	fs := flag.NewFlagSet("stop-daemon", flag.ExitOnError)
	force := fs.Bool("force", false, "Force stop daemon without interactive confirmation")
	forceShort := fs.Bool("f", false, "Shorthand for --force")
	yes := fs.Bool("yes", false, "Shorthand for --force")
	yesShort := fs.Bool("y", false, "Shorthand for --force")
	_ = fs.Parse(args)

	homeDir, err := store.HomeDir()
	if err != nil {
		fatalf("%v", err)
	}
	client := daemon.NewClient(store.SocketPath(homeDir))
	info, err := client.HealthInfo()
	if err != nil {
		fmt.Println("aio daemon is not running.")
		return
	}

	skipConfirm := *force || *forceShort || *yes || *yesShort
	if !skipConfirm {
		snap, _ := client.State()
		busyLines, idleCount := summarizeActiveRunsForStop(snap, info.ActiveRuns)
		if len(busyLines) > 0 || idleCount > 0 || info.ActiveRuns > 0 {
			fmt.Printf("⚠ 检测到后台 aio daemon (pid %d) 仍有 %d 个活跃终端会话：\n", info.PID, max(info.ActiveRuns, len(busyLines)))
			for _, line := range busyLines {
				fmt.Printf("  • %s\n", line)
			}
			if idleCount > 0 {
				fmt.Printf("  • 另有 %d 个已打开的空闲 Agent / Shell 终端\n", idleCount)
			}
			fmt.Println("停止 daemon 会立即中断上述进程（项目、代码改动和已绑定 Session ID 会保留，重开后按 Enter 可续聊）。")

			if !isInteractiveStdin() {
				fmt.Fprintln(os.Stderr, "非交互终端已取消停止；若确认强制停止，请执行：aio stop-daemon --force")
				os.Exit(1)
			}
			if !confirmStopPrompt(os.Stdin, os.Stdout) {
				fmt.Println("已取消，后台 daemon 与任务继续保持运行。")
				return
			}
		}
	}

	if err := client.Shutdown(); err != nil {
		fmt.Printf("daemon not running or failed to stop: %v\n", err)
		return
	}
	fmt.Println("aio daemon stopped.")
}

func summarizeActiveRunsForStop(snap protocol.StateSnapshot, activePTYs int) (busyLines []string, otherActive int) {
	projNames := make(map[string]string, len(snap.Projects))
	for _, p := range snap.Projects {
		projNames[p.ID] = p.Name
	}
	taskTitles := make(map[string]string, len(snap.Tasks))
	for _, t := range snap.Tasks {
		taskTitles[t.ID] = t.Title
	}
	for _, r := range snap.Runs {
		switch r.Status {
		case protocol.StatusRunning, protocol.StatusStarting, protocol.StatusWaitingInput:
			statusCN := "运行中"
			if r.Status == protocol.StatusWaitingInput {
				statusCN = "等待确认"
			} else if r.Status == protocol.StatusStarting {
				statusCN = "启动中"
			}
			proj := projNames[r.ProjectID]
			task := taskTitles[r.TaskID]
			label := r.Agent
			if proj != "" && task != "" {
				label = fmt.Sprintf("%s / %s / %s", proj, task, r.Agent)
			} else if task != "" {
				label = fmt.Sprintf("%s / %s", task, r.Agent)
			}
			busyLines = append(busyLines, fmt.Sprintf("%s [%s]", label, statusCN))
		}
	}
	if activePTYs > len(busyLines) {
		otherActive = activePTYs - len(busyLines)
	}
	return busyLines, otherActive
}

func isInteractiveStdin() bool {
	return term.IsTerminal(os.Stdin.Fd())
}

func confirmStopPrompt(in io.Reader, out io.Writer) bool {
	_, _ = fmt.Fprint(out, "确认强制停止 daemon 并中断以上会话吗？[y/N]: ")
	reader := bufio.NewReader(in)
	line, err := reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return false
	}
	ans := strings.ToLower(strings.TrimSpace(line))
	return ans == "y" || ans == "yes"
}

func runTUI(noMouse bool) {
	homeDir, err := store.HomeDir()
	if err != nil {
		fatalf("resolve home dir: %v", err)
	}
	sockPath := store.SocketPath(homeDir)
	client := daemon.NewClient(sockPath)
	if err := client.EnsureDaemon(homeDir); err != nil {
		fatalf("failed to connect/start aio daemon: %v", err)
	}
	// Sync current shell's proxy environment variables (HTTP_PROXY/HTTPS_PROXY/NO_PROXY)
	// into the background daemon so stopping/restarting runs or aio never loses proxy settings.
	_ = client.SyncProxyEnv()

	colorProf := tui.ConfigureColorProfile()
	cursorSync := tui.NewCursorSyncWriter(os.Stdout)
	defer cursorSync.Clear()

	m := tui.NewModel(client, cursorSync)
	m.SetColorProfile(colorProf)
	opts := []tea.ProgramOption{tea.WithOutput(cursorSync), tea.WithAltScreen()}
	if noMouse {
		m.SetMouseDisabled(true)
	} else {
		opts = append(opts, tea.WithMouseCellMotion())
	}
	p := tea.NewProgram(m, opts...)
	if _, err := p.Run(); err != nil {
		fatalf("tui error: %v", err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "aio: "+format+"\n", args...)
	os.Exit(1)
}
