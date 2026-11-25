package daemon

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/loucongV/all-in-one/internal/adapter"
	"github.com/loucongV/all-in-one/internal/protocol"
)

func (s *Server) handleGetProxyConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Config().Proxy)
}

func (s *Server) handleUpdateProxyConfig(w http.ResponseWriter, r *http.Request) {
	var req protocol.ProxyConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	saved, err := s.store.UpdateProxyConfig(req)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.applyConfiguredProxy(saved)
	s.broadcast(protocol.DaemonEvent{Type: "state_changed", At: time.Now()})
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleTestProxyConfig(w http.ResponseWriter, r *http.Request) {
	var req protocol.ProxyConfig
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	if req.Mode == "" {
		req = s.store.Config().Proxy
	}
	res := TestProxyConnectivity(req, 1500*time.Millisecond)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) applyConfiguredProxy(cfg protocol.ProxyConfig) {
	switch cfg.Mode {
	case protocol.ProxyModeManual:
		raw := map[string]string{
			"HTTP_PROXY":  cfg.HTTPProxy,
			"HTTPS_PROXY": cfg.HTTPSProxy,
			"ALL_PROXY":   cfg.AllProxy,
			"NO_PROXY":    cfg.NoProxy,
		}
		if strings.TrimSpace(raw["HTTPS_PROXY"]) == "" && strings.TrimSpace(raw["HTTP_PROXY"]) != "" {
			raw["HTTPS_PROXY"] = raw["HTTP_PROXY"]
		}
		if norm := ApplyProxyEnv(raw); len(norm) > 0 {
			SaveProxyEnv(s.store.HomeDir(), norm)
		}
	case protocol.ProxyModeDirect:
		// Keep daemon env untouched or rely on per-process unset overrides; no dead cache write.
	}
}

func (s *Server) handleSetAgentBinary(w http.ResponseWriter, r *http.Request) {
	var req protocol.SetAgentBinaryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, errors.New("tool name is required"))
		return
	}
	if name != "magpie" {
		if _, err := s.registry.Get(name); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	binPath := strings.TrimSpace(req.Path)
	if binPath != "" {
		// Validate path exists before persisting so typos are caught immediately.
		expanded := binPath
		if strings.HasPrefix(expanded, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				expanded = home + expanded[1:]
			}
		}
		fi, err := os.Stat(expanded)
		if err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("路径不存在: %s", binPath))
			return
		}
		if !fi.IsDir() && fi.Mode()&0o111 == 0 {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("文件没有可执行权限: %s", binPath))
			return
		}
	}
	if err := s.store.SetAgentBinary(name, binPath); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	adapter.SetBinaryOverride(name, binPath)
	s.broadcast(protocol.DaemonEvent{Type: "state_changed", At: time.Now()})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"agents": s.registry.ListInfo(),
		"magpie": adapter.MagpieStatus(),
	})
}

func (s *Server) handleInstallAgent(w http.ResponseWriter, r *http.Request) {
	var req protocol.InstallAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	name := strings.TrimSpace(req.Name)
	installCmd := adapter.DefaultInstallCommand(name)
	sessionID := "install-" + name

	// If an installer for this tool is already running, reattach to it instead of killing and restarting.
	if existing, ok := s.pty.Get(sessionID); ok {
		if !existing.IsClosed() {
			writeJSON(w, http.StatusOK, protocol.InstallAgentResponse{
				SessionID:  sessionID,
				Name:       name,
				Command:    installCmd,
				Running:    true,
				Reattached: true,
			})
			return
		}
		if req.AttachOnly {
			writeJSON(w, http.StatusOK, protocol.InstallAgentResponse{
				SessionID:  sessionID,
				Name:       name,
				Command:    installCmd,
				Running:    false,
				Reattached: true,
			})
			return
		}
	} else if req.AttachOnly {
		writeErr(w, http.StatusNotFound, fmt.Errorf("%s 当前没有可查看的安装日志", name))
		return
	}

	if installCmd == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("%s 暂不支持自动安装，请使用「自选路径」指定已有二进制", name))
		return
	}

	shBin := "bash"
	if p, err := exec.LookPath("bash"); err == nil {
		shBin = p
	} else if p, err := exec.LookPath("sh"); err == nil {
		shBin = p
	}

	homeDir, _ := os.UserHomeDir()
	if homeDir == "" {
		homeDir = s.store.HomeDir()
	}

	// Stream command output transparently in a PTY with pipefail enabled so curl/network
	// failures in `curl ... | sh` pipelines never masquerade as exit=0. If installation fails
	// while a proxy is active (e.g. proxy TLS reset against Cloudflare), automatically retry direct once.
	innerCmd := fmt.Sprintf(`set -o pipefail 2>/dev/null || true; %s`, installCmd)
	script := fmt.Sprintf(
		`set -o pipefail 2>/dev/null || true; printf "\033[1;36m==> 正在安装 %s\033[0m\n$ %%s\n\n" %q; %s; rc=$?; if [ $rc -ne 0 ] && [ -n "${https_proxy:-}${HTTPS_PROXY:-}${http_proxy:-}${HTTP_PROXY:-}${all_proxy:-}${ALL_PROXY:-}" ]; then printf "\n\033[1;33m⚠ 经代理安装失败 (exit=$rc)，正在自动尝试直连重试...\033[0m\n\n"; env -u http_proxy -u https_proxy -u HTTP_PROXY -u HTTPS_PROXY -u all_proxy -u ALL_PROXY %q -c %q; rc=$?; fi; echo ""; if [ $rc -eq 0 ]; then printf "\033[1;32m✓ %s 安装完成 (exit=0)！\033[0m\n"; else printf "\033[1;31m✗ %s 安装失败 (exit=$rc)，请检查上方日志或在「, 控制中心」调整代理配置。\033[0m\n"; fi; exit $rc`,
		name, installCmd, installCmd, shBin, innerCmd, name, name,
	)

	spec := adapter.CommandSpec{
		Binary: shBin,
		Args:   []string{"-c", script},
		Dir:    homeDir,
		Env:    ProxyEnvOverridesForAgent(name, s.store.Config().Proxy),
	}
	sess, err := s.pty.Start(sessionID, spec)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("start installer: %w", err))
		return
	}
	// Notify TUI clients immediately so tools modal & footer reflect the active background install.
	s.broadcast(protocol.DaemonEvent{Type: "state_changed", At: time.Now()})
	// Broadcast state refresh when the installer process exits so the sidebar/modal picks up the new binary.
	go func() {
		<-sess.Done()
		s.broadcast(protocol.DaemonEvent{Type: "state_changed", At: time.Now()})
	}()

	writeJSON(w, http.StatusOK, protocol.InstallAgentResponse{
		SessionID: sessionID,
		Name:      name,
		Command:   installCmd,
		Running:   true,
	})
}

func (s *Server) handleEnsureMagpieTUI(w http.ResponseWriter, r *http.Request) {
	magpieBin, ok := adapter.ResolveMagpie()
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("未检测到 magpie，请在「工具管理 (I)」中安装或指定路径"))
		return
	}
	homeDir, _ := os.UserHomeDir()
	if homeDir == "" {
		homeDir = s.store.HomeDir()
	}
	const sessionID = "magpie-tui"
	if sess, exists := s.pty.Get(sessionID); !exists || sess.IsClosed() {
		if _, err := s.pty.Start(sessionID, adapter.CommandSpec{
			Binary: magpieBin,
			Args:   []string{"tui"},
			Dir:    homeDir,
			Env:    ProxyEnvOverridesForAgent("magpie", s.store.Config().Proxy),
		}); err != nil {
			writeErr(w, http.StatusInternalServerError, fmt.Errorf("start magpie tui: %w", err))
			return
		}
	}
	writeJSON(w, http.StatusOK, protocol.EnsureShellResponse{
		ShellID: sessionID,
		Dir:     homeDir,
		Shell:   "magpie tui",
	})
}

func (s *Server) handleMobileStatus(w http.ResponseWriter, r *http.Request) {
	if s.mobile == nil {
		writeJSON(w, http.StatusOK, protocol.MobileStatus{Running: false})
		return
	}
	writeJSON(w, http.StatusOK, s.mobile.Status())
}

func (s *Server) handleMobileStart(w http.ResponseWriter, r *http.Request) {
	if s.mobile == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("mobile bridge not initialized"))
		return
	}
	var req protocol.MobileStartRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	port := req.Port
	if port == 0 {
		if cfgPort := s.store.Config().MobilePort; cfgPort > 0 {
			port = cfgPort
		} else {
			port = 19820 // Preferred stable LAN port (auto-falls back to ephemeral if busy).
		}
	}
	st, err := s.mobile.Start(port, req.RotateToken, req.HTTPS)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.broadcast(protocol.DaemonEvent{Type: "state_changed", At: time.Now()})
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleVoiceTranscribe(w http.ResponseWriter, r *http.Request) {
	cmdTpl := strings.TrimSpace(s.store.Config().VoiceTranscribeCmd)
	if cmdTpl == "" {
		writeErr(w, http.StatusNotImplemented, errors.New("未配置服务端语音转写命令（可在 ~/.aio/config.toml 中配置 voice_transcribe_cmd）"))
		return
	}
	const maxAudioBytes = 15 << 20 // 15 MiB limit
	r.Body = http.MaxBytesReader(w, r.Body, maxAudioBytes)

	var audioBytes []byte
	ext := ".webm"
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(maxAudioBytes); err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("解析音频表单失败: %w", err))
			return
		}
		file, hdr, err := r.FormFile("audio")
		if err != nil {
			writeErr(w, http.StatusBadRequest, errors.New("缺少 audio 字段"))
			return
		}
		defer file.Close()
		audioBytes, err = io.ReadAll(io.LimitReader(file, maxAudioBytes))
		if err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("读取音频失败: %w", err))
			return
		}
		if hdr != nil {
			ext = sanitizeAudioExt(filepath.Ext(hdr.Filename))
		}
	} else {
		var err error
		audioBytes, err = io.ReadAll(io.LimitReader(r.Body, maxAudioBytes))
		if err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("读取音频失败: %w", err))
			return
		}
		switch {
		case strings.Contains(ct, "mp4"), strings.Contains(ct, "m4a"):
			ext = ".m4a"
		case strings.Contains(ct, "wav"):
			ext = ".wav"
		case strings.Contains(ct, "mpeg"), strings.Contains(ct, "mp3"):
			ext = ".mp3"
		case strings.Contains(ct, "ogg"):
			ext = ".ogg"
		}
	}
	if len(audioBytes) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("音频内容为空"))
		return
	}

	tmpDir, err := os.MkdirTemp("", "aio-voice-*")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer os.RemoveAll(tmpDir)

	audioPath := filepath.Join(tmpDir, "input"+ext)
	if err := os.WriteFile(audioPath, audioBytes, 0o600); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	// Substitute {file} with a shell variable reference to eliminate injection risk.
	script := strings.ReplaceAll(cmdTpl, "{file}", `"$AIO_AUDIO_FILE"`)
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Env = append(os.Environ(), "AIO_AUDIO_FILE="+audioPath)
	out, err := cmd.Output()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("语音转写命令执行失败: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"text": strings.TrimSpace(string(out)),
	})
}

func sanitizeAudioExt(ext string) string {
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case ".webm", ".m4a", ".mp4", ".wav", ".mp3", ".ogg", ".opus", ".aac", ".flac":
		return strings.ToLower(strings.TrimSpace(ext))
	default:
		return ".webm"
	}
}

func (s *Server) handleMobileStop(w http.ResponseWriter, r *http.Request) {
	if s.mobile != nil {
		s.mobile.Stop()
	}
	s.broadcast(protocol.DaemonEvent{Type: "state_changed", At: time.Now()})
	writeJSON(w, http.StatusOK, protocol.MobileStatus{Running: false})
}

func (s *Server) handleMobileSelectIP(w http.ResponseWriter, r *http.Request) {
	if s.mobile == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("mobile bridge not initialized"))
		return
	}
	var req protocol.MobileSelectIPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	st := s.mobile.SelectIP(req.IP)
	s.broadcast(protocol.DaemonEvent{Type: "state_changed", At: time.Now()})
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleMobileRevokeSession(w http.ResponseWriter, r *http.Request) {
	if s.mobile == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("mobile bridge not initialized"))
		return
	}
	var req protocol.MobileRevokeSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var st protocol.MobileStatus
	if req.All {
		st = s.mobile.RevokeAllSessions()
	} else {
		sid := strings.TrimSpace(req.SessionID)
		if sid == "" {
			writeErr(w, http.StatusBadRequest, errors.New("session_id is required"))
			return
		}
		st = s.mobile.RevokeSession(sid)
	}
	s.broadcast(protocol.DaemonEvent{Type: "state_changed", At: time.Now()})
	writeJSON(w, http.StatusOK, st)
}

func wantsCleanSession(r *http.Request) bool {
	v := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("clean_session")))
	return v == "1" || v == "true" || v == "yes"
}

// cleanupUnusedShell stops and removes any persistent workspace shell whose directory
// is no longer referenced by any remaining project or run.
func (s *Server) cleanupUnusedShell(dir string) {
	abs := cleanAbs(dir)
	if abs == "" {
		return
	}
	snap := s.store.Snapshot(nil)
	for _, p := range snap.Projects {
		if cleanAbs(p.RepoPath) == abs {
			return
		}
	}
	for _, r := range snap.Runs {
		if cleanAbs(r.WorktreePath) == abs {
			return
		}
	}
	sum := md5.Sum([]byte(abs))
	shellID := "shell-" + hex.EncodeToString(sum[:5])
	_ = s.pty.Remove(shellID)
}

// prepareRunCommandSpec wraps a CommandSpec with optional Magpie routing and per-agent proxy env overrides.
func (s *Server) prepareRunCommandSpec(agentName, magpieModel string, spec adapter.CommandSpec) adapter.CommandSpec {
	if strings.TrimSpace(magpieModel) != "" {
		if magpieBin, ok := adapter.ResolveMagpie(); ok {
			spec = adapter.WrapWithMagpie(magpieBin, agentName, magpieModel, spec)
		}
	}
	if proxyEnv := ProxyEnvOverridesForAgent(agentName, s.store.Config().Proxy); len(proxyEnv) > 0 {
		spec.Env = append(spec.Env, proxyEnv...)
	}
	return spec
}
