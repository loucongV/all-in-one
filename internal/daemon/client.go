package daemon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/loucongV/all-in-one/internal/protocol"
)

type Client struct {
	sockPath string
	http     *http.Client
	dialer   *websocket.Dialer

	mu      sync.RWMutex
	homeDir string
}

func NewClient(sockPath string) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sockPath)
		},
	}
	wsDialer := &websocket.Dialer{
		NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sockPath)
		},
		HandshakeTimeout: 3 * time.Second,
	}
	return &Client{
		sockPath: sockPath,
		http:     &http.Client{Transport: transport, Timeout: 15 * time.Second},
		dialer:   wsDialer,
	}
}

type HealthResponse struct {
	OK             bool   `json:"ok"`
	PID            int    `json:"pid"`
	Sock           string `json:"sock"`
	Version        string `json:"version,omitempty"`
	BinFingerprint string `json:"bin_fingerprint"`
	ActiveRuns     int    `json:"active_runs"`
}

func BinFingerprint(exePath string) string {
	if exePath == "" {
		return ""
	}
	fi, err := os.Stat(exePath)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d", fi.Size(), fi.ModTime().UnixNano())
}

func (c *Client) HealthInfo() (HealthResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://daemon/api/health", nil)
	if err != nil {
		return HealthResponse{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return HealthResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return HealthResponse{}, fmt.Errorf("health status %d", resp.StatusCode)
	}
	var info HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return HealthResponse{}, err
	}
	return info, nil
}

func (c *Client) Health() error {
	_, err := c.HealthInfo()
	return err
}

// EnsureDaemon pings the daemon and auto-spawns a detached `aio daemon` if it is not running.
// If the running daemon is from an older binary and has 0 active runs, it restarts seamlessly.
func (c *Client) EnsureDaemon(homeDir string) error {
	c.mu.Lock()
	c.homeDir = homeDir
	c.mu.Unlock()

	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		return err
	}
	// Serialize concurrent daemon start/restart attempts across processes.
	lockPath := filepath.Join(homeDir, "daemon.lock")
	if lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644); err == nil {
		_ = syscall.Flock(int(lf.Fd()), syscall.LOCK_EX)
		defer func() {
			_ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
			_ = lf.Close()
		}()
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	wantFP := BinFingerprint(exe)

	if info, err := c.HealthInfo(); err == nil {
		// Daemon is running; if it's up-to-date or still has active agent runs, keep it running.
		if info.BinFingerprint == wantFP || info.ActiveRuns > 0 {
			return nil
		}
		// Stale idle daemon: shut it down cleanly and wait for its PID to exit before spawning the new one.
		oldPID := info.PID
		_ = c.Shutdown()
		waitDown := time.Now().Add(3 * time.Second)
		for time.Now().Before(waitDown) {
			alive := oldPID > 0 && syscall.Kill(oldPID, 0) == nil
			if !alive && c.Health() != nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	logFile, err := os.OpenFile(filepath.Join(homeDir, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}

	cmd := exec.Command(exe, "daemon", "--sock", c.sockPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("start daemon: %w", err)
	}
	_ = logFile.Close()

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if err := c.Health(); err == nil {
			return nil
		}
		time.Sleep(80 * time.Millisecond)
	}
	return errors.New("timed out waiting for aio daemon to start")
}

func (c *Client) getHomeDir() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.homeDir
}

func isDaemonUnreachable(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, os.ErrNotExist) ||
		errors.Is(err, syscall.ENOENT) ||
		errors.Is(err, syscall.ECONNREFUSED)
}

func (c *Client) doJSON(method, path string, in any, out any) error {
	var rawBody []byte
	if in != nil {
		var err error
		rawBody, err = json.Marshal(in)
		if err != nil {
			return err
		}
	}

	sendOnce := func() (*http.Response, error) {
		var body io.Reader
		if rawBody != nil {
			body = bytes.NewReader(rawBody)
		}
		req, err := http.NewRequest(method, "http://daemon"+path, body)
		if err != nil {
			return nil, err
		}
		if rawBody != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return c.http.Do(req)
	}

	resp, err := sendOnce()
	if err != nil && isDaemonUnreachable(err) && path != "/api/shutdown" && path != "/api/health" {
		if hd := c.getHomeDir(); hd != "" {
			if ensureErr := c.EnsureDaemon(hd); ensureErr == nil {
				resp, err = sendOnce()
			}
		}
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		var apiErr struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(respBytes, &apiErr) == nil && apiErr.Error != "" {
			return errors.New(apiErr.Error)
		}
		return fmt.Errorf("daemon HTTP %d: %s", resp.StatusCode, string(respBytes))
	}
	if out != nil && len(respBytes) > 0 {
		return json.Unmarshal(respBytes, out)
	}
	return nil
}

func (c *Client) State() (protocol.StateSnapshot, error) {
	var snap protocol.StateSnapshot
	err := c.doJSON(http.MethodGet, "/api/state", nil, &snap)
	return snap, err
}

func (c *Client) AddProject(req protocol.AddProjectRequest) (protocol.Project, error) {
	var proj protocol.Project
	err := c.doJSON(http.MethodPost, "/api/projects", req, &proj)
	return proj, err
}

func (c *Client) DeleteProject(id string, cleanSession ...bool) error {
	path := "/api/projects/" + id
	if len(cleanSession) > 0 && cleanSession[0] {
		path += "?clean_session=1"
	}
	return c.doJSON(http.MethodDelete, path, nil, nil)
}

func (c *Client) CreateTask(req protocol.CreateTaskRequest) (protocol.CreateTaskResponse, error) {
	var resp protocol.CreateTaskResponse
	err := c.doJSON(http.MethodPost, "/api/tasks", req, &resp)
	return resp, err
}

func (c *Client) RenameTask(id, title string) (protocol.Task, error) {
	var task protocol.Task
	err := c.doJSON(http.MethodPatch, "/api/tasks/"+id, protocol.RenameTaskRequest{Title: title}, &task)
	return task, err
}

func (c *Client) AddTaskRuns(taskID string, req protocol.AddRunsRequest) ([]protocol.Run, error) {
	var runs []protocol.Run
	err := c.doJSON(http.MethodPost, "/api/tasks/"+taskID+"/runs", req, &runs)
	return runs, err
}

func (c *Client) DeleteRun(id string, cleanSession ...bool) error {
	path := "/api/runs/" + id
	if len(cleanSession) > 0 && cleanSession[0] {
		path += "?clean_session=1"
	}
	return c.doJSON(http.MethodDelete, path, nil, nil)
}

func (c *Client) AckRun(id string) error {
	return c.doJSON(http.MethodPost, "/api/runs/"+id+"/ack", nil, nil)
}

func (c *Client) ProbeQuota(runID string) ([]protocol.AgentQuota, error) {
	var out []protocol.AgentQuota
	err := c.doJSON(http.MethodPost, "/api/quota/probe", map[string]string{"run_id": runID}, &out)
	return out, err
}

func (c *Client) DeleteTask(id string, cleanSession ...bool) error {
	path := "/api/tasks/" + id
	if len(cleanSession) > 0 && cleanSession[0] {
		path += "?clean_session=1"
	}
	return c.doJSON(http.MethodDelete, path, nil, nil)
}

func (c *Client) TaskDiff(id string) (protocol.TaskDiffResponse, error) {
	var resp protocol.TaskDiffResponse
	err := c.doJSON(http.MethodGet, "/api/tasks/"+id+"/diff", nil, &resp)
	return resp, err
}

func (c *Client) AdoptTask(id string, req protocol.AdoptTaskRequest) (protocol.AdoptTaskResponse, error) {
	var resp protocol.AdoptTaskResponse
	err := c.doJSON(http.MethodPost, "/api/tasks/"+id+"/adopt", req, &resp)
	return resp, err
}

func (c *Client) StopRun(id string) error {
	return c.doJSON(http.MethodPost, "/api/runs/"+id+"/stop", nil, nil)
}

func (c *Client) RestartRun(id string, req protocol.RestartRunRequest) (protocol.Run, error) {
	var run protocol.Run
	err := c.doJSON(http.MethodPost, "/api/runs/"+id+"/restart", req, &run)
	return run, err
}

func (c *Client) ListRunSessions(id string) ([]protocol.CLISessionInfo, error) {
	var out []protocol.CLISessionInfo
	err := c.doJSON(http.MethodGet, "/api/runs/"+id+"/sessions", nil, &out)
	return out, err
}

func (c *Client) BindRunSession(id, sessionID string) (protocol.Run, error) {
	var run protocol.Run
	err := c.doJSON(http.MethodPatch, "/api/runs/"+id+"/session", protocol.BindSessionRequest{SessionID: sessionID}, &run)
	return run, err
}

func (c *Client) EnsureShell(dir string) (protocol.EnsureShellResponse, error) {
	return c.EnsureShellSized(dir, 0, 0)
}

func (c *Client) EnsureShellSized(dir string, cols, rows int) (protocol.EnsureShellResponse, error) {
	var resp protocol.EnsureShellResponse
	err := c.doJSON(http.MethodPost, "/api/shell/ensure", protocol.EnsureShellRequest{
		Dir:  dir,
		Cols: cols,
		Rows: rows,
	}, &resp)
	return resp, err
}

func (c *Client) SyncProxyEnv() error {
	return c.doJSON(http.MethodPost, "/api/env/sync", protocol.EnvSyncRequest{
		Proxy: CaptureProxyEnv(),
		Path:  os.Getenv("PATH"),
	}, nil)
}

func (c *Client) UpdateProxyConfig(cfg protocol.ProxyConfig) (protocol.ProxyConfig, error) {
	var saved protocol.ProxyConfig
	err := c.doJSON(http.MethodPut, "/api/config/proxy", cfg, &saved)
	return saved, err
}

func (c *Client) TestProxyConfig(cfg protocol.ProxyConfig) (protocol.ProxyTestResult, error) {
	var res protocol.ProxyTestResult
	err := c.doJSON(http.MethodPost, "/api/config/proxy/test", cfg, &res)
	return res, err
}

func (c *Client) SetAgentBinary(name, binPath string) error {
	return c.doJSON(http.MethodPost, "/api/agents/binary", protocol.SetAgentBinaryRequest{
		Name: name,
		Path: binPath,
	}, nil)
}

func (c *Client) InstallAgent(name string) (protocol.InstallAgentResponse, error) {
	var resp protocol.InstallAgentResponse
	err := c.doJSON(http.MethodPost, "/api/agents/install", protocol.InstallAgentRequest{
		Name: name,
	}, &resp)
	return resp, err
}

func (c *Client) AttachInstallAgent(name string) (protocol.InstallAgentResponse, error) {
	var resp protocol.InstallAgentResponse
	err := c.doJSON(http.MethodPost, "/api/agents/install", protocol.InstallAgentRequest{
		Name:       name,
		AttachOnly: true,
	}, &resp)
	return resp, err
}

func (c *Client) EnsureMagpieTUI() (protocol.EnsureShellResponse, error) {
	var resp protocol.EnsureShellResponse
	err := c.doJSON(http.MethodPost, "/api/magpie/tui", nil, &resp)
	return resp, err
}

func (c *Client) MobileStatus() (protocol.MobileStatus, error) {
	var st protocol.MobileStatus
	err := c.doJSON(http.MethodGet, "/api/mobile/status", nil, &st)
	return st, err
}

func (c *Client) MobileStart(port int, rotateToken bool, https bool) (protocol.MobileStatus, error) {
	var st protocol.MobileStatus
	err := c.doJSON(http.MethodPost, "/api/mobile/start", protocol.MobileStartRequest{
		Port:        port,
		RotateToken: rotateToken,
		HTTPS:       https,
	}, &st)
	return st, err
}

func (c *Client) MobileStop() (protocol.MobileStatus, error) {
	var st protocol.MobileStatus
	err := c.doJSON(http.MethodPost, "/api/mobile/stop", nil, &st)
	return st, err
}

func (c *Client) MobileSelectIP(ip string) (protocol.MobileStatus, error) {
	var st protocol.MobileStatus
	err := c.doJSON(http.MethodPost, "/api/mobile/select-ip", protocol.MobileSelectIPRequest{
		IP: ip,
	}, &st)
	return st, err
}

func (c *Client) MobileRevokeSession(sessionID string, all bool) (protocol.MobileStatus, error) {
	var st protocol.MobileStatus
	err := c.doJSON(http.MethodPost, "/api/mobile/sessions/revoke", protocol.MobileRevokeSessionRequest{
		SessionID: sessionID,
		All:       all,
	}, &st)
	return st, err
}

func (c *Client) Shutdown() error {
	return c.doJSON(http.MethodPost, "/api/shutdown", nil, nil)
}

func (c *Client) SendHook(req protocol.HookEventRequest) error {
	shortHTTP := &http.Client{
		Transport: c.http.Transport,
		Timeout:   400 * time.Millisecond,
	}
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequest(http.MethodPost, "http://daemon/api/hooks", bytes.NewReader(b))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := shortHTTP.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (c *Client) ConnectEvents() (*websocket.Conn, error) {
	conn, _, err := c.dialer.Dial("ws://daemon/ws/events", nil)
	if err != nil && isDaemonUnreachable(err) {
		if hd := c.getHomeDir(); hd != "" {
			if ensureErr := c.EnsureDaemon(hd); ensureErr == nil {
				conn, _, err = c.dialer.Dial("ws://daemon/ws/events", nil)
			}
		}
	}
	return conn, err
}

func (c *Client) ConnectPTY(runID string, raw bool) (*websocket.Conn, error) {
	u := fmt.Sprintf("ws://daemon/ws/runs/%s/pty", runID)
	if raw {
		u += "?mode=raw"
	}
	conn, _, err := c.dialer.Dial(u, nil)
	if err != nil && isDaemonUnreachable(err) {
		if hd := c.getHomeDir(); hd != "" {
			if ensureErr := c.EnsureDaemon(hd); ensureErr == nil {
				conn, _, err = c.dialer.Dial(u, nil)
			}
		}
	}
	return conn, err
}

func SendPTYInput(conn *websocket.Conn, data []byte) error {
	if conn == nil {
		return nil
	}
	return conn.WriteJSON(protocol.PTYMessage{
		Type: "input",
		Data: base64.StdEncoding.EncodeToString(data),
	})
}

func SendPTYPaste(conn *websocket.Conn, text string) error {
	if conn == nil || text == "" {
		return nil
	}
	return conn.WriteJSON(protocol.PTYMessage{
		Type: "paste",
		Data: base64.StdEncoding.EncodeToString([]byte(text)),
	})
}

func SendPTYScroll(conn *websocket.Conn, delta, relX, relY int) error {
	if conn == nil {
		return nil
	}
	return conn.WriteJSON(protocol.PTYMessage{
		Type:  "scroll",
		Delta: delta,
		X:     relX,
		Y:     relY,
	})
}

func SendPTYResize(conn *websocket.Conn, cols, rows int) error {
	if conn == nil {
		return nil
	}
	return conn.WriteJSON(protocol.PTYMessage{
		Type: "resize",
		Cols: cols,
		Rows: rows,
	})
}
