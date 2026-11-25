package daemon

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/gorilla/websocket"
	"github.com/loucongV/all-in-one/internal/adapter"
	"github.com/loucongV/all-in-one/internal/mobile"
	"github.com/loucongV/all-in-one/internal/protocol"
	"github.com/loucongV/all-in-one/internal/ptymgr"
	"github.com/loucongV/all-in-one/internal/quota"
	"github.com/loucongV/all-in-one/internal/store"
	"github.com/loucongV/all-in-one/internal/version"
	"github.com/loucongV/all-in-one/internal/worktree"
)

type Server struct {
	store          *store.Store
	registry       *adapter.Registry
	pty            *ptymgr.Manager
	quota          *quota.Collector
	mobile         *mobile.Bridge
	sockPath       string
	selfBin        string
	binFingerprint string

	httpSrv      *http.Server
	listener     net.Listener
	sockInfo     os.FileInfo
	shuttingDown atomic.Bool
	shutdownOnce sync.Once

	evMu   sync.Mutex
	evSubs map[chan protocol.DaemonEvent]struct{}
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func NewServer(st *store.Store, reg *adapter.Registry, sockPath, selfBin string) *Server {
	if reg == nil {
		reg = adapter.DefaultRegistry()
	}
	if sockPath == "" {
		sockPath = store.SocketPath(st.HomeDir())
	}
	if selfBin == "" {
		selfBin, _ = os.Executable()
	}
	s := &Server{
		store:          st,
		registry:       reg,
		sockPath:       sockPath,
		selfBin:        selfBin,
		binFingerprint: BinFingerprint(selfBin),
		evSubs:         make(map[chan protocol.DaemonEvent]struct{}),
	}
	s.pty = ptymgr.NewManager(s.handleRunStatusChange)
	s.quota = quota.NewCollector(st.HomeDir(), st.Config().QuotaCommands, func() {
		s.broadcast(protocol.DaemonEvent{Type: "state_changed", At: time.Now()})
	})
	s.quota.SetActiveAgentsFunc(s.activeQuotaAgents)
	s.pty.SetRateLimitCallback(func(runID, detail string) {
		if run, ok := s.store.GetRun(runID); ok {
			s.quota.MarkRateLimited(run.Agent, detail)
		}
	})
	s.pty.SetUsageCallback(func(runID, plainScreen string) {
		if run, ok := s.store.GetRun(runID); ok {
			s.quota.IngestScreen(run.Agent, plainScreen)
			s.reconcileCodexStatusFromScreen(run, plainScreen)
		}
	})
	cfg := st.Config()
	adapter.SetBinaryOverrides(cfg.AgentBinaries, cfg.MagpieBinary)
	_ = st.MarkOrphanedRunsStopped()
	s.initProxyEnv()
	s.refreshProjectRepoFlags()
	return s
}

func (s *Server) initProxyEnv() {
	cfg := s.store.Config()
	if cfg.Proxy.Mode == protocol.ProxyModeManual {
		s.applyConfiguredProxy(cfg.Proxy)
		return
	}
	if cfg.Proxy.Mode == protocol.ProxyModeDirect {
		return
	}
	homeDir := s.store.HomeDir()
	cur := CaptureProxyEnv()
	if hasActiveProxyURL(cur) {
		ApplyProxyEnv(cur)
		SaveProxyEnv(homeDir, cur)
		return
	}
	// If daemon started from a shell/context without proxy env (e.g. after stop & restart),
	// restore the last working proxy snapshot only if that proxy port is actively listening.
	if saved := LoadProxyEnv(homeDir); hasActiveProxyURL(saved) && ProxyEndpointReachable(saved, 150*time.Millisecond) {
		ApplyProxyEnv(saved)
	}
}

func (s *Server) refreshProjectRepoFlags() {
	snap := s.store.Snapshot(nil)
	for _, p := range snap.Projects {
		info, err := worktree.InspectWorkspace(p.RepoPath)
		if err != nil {
			continue
		}
		isMulti := false
		subCount := 0
		if !info.IsGitRepo {
			subs := worktree.InspectSubRepos(info.Root)
			if len(subs) > 0 {
				isMulti = true
				subCount = len(subs)
			}
		}
		if p.IsGitRepo != info.IsGitRepo || p.IsMultiRepo != isMulti || p.SubRepoCount != subCount {
			p.IsGitRepo = info.IsGitRepo
			p.IsMultiRepo = isMulti
			p.SubRepoCount = subCount
			_ = s.store.UpdateProject(p)
		}
	}
}

func (s *Server) Start(ctx context.Context) error {
	go s.quota.StartLoop(ctx)
	if err := os.MkdirAll(filepath.Dir(s.sockPath), 0o755); err != nil {
		return err
	}
	_ = os.Remove(s.sockPath)

	ln, err := net.Listen("unix", s.sockPath)
	if err != nil {
		return fmt.Errorf("listen unix %s: %w", s.sockPath, err)
	}
	if uln, ok := ln.(*net.UnixListener); ok {
		// Prevent Go's automatic unlink-on-close from deleting a newer daemon's socket file.
		uln.SetUnlinkOnClose(false)
	}
	s.sockInfo, _ = os.Stat(s.sockPath)
	s.listener = ln

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("POST /api/env/sync", s.handleSyncEnv)
	mux.HandleFunc("GET /api/config/proxy", s.handleGetProxyConfig)
	mux.HandleFunc("PUT /api/config/proxy", s.handleUpdateProxyConfig)
	mux.HandleFunc("POST /api/config/proxy/test", s.handleTestProxyConfig)
	mux.HandleFunc("POST /api/agents/binary", s.handleSetAgentBinary)
	mux.HandleFunc("POST /api/agents/install", s.handleInstallAgent)
	mux.HandleFunc("POST /api/magpie/tui", s.handleEnsureMagpieTUI)
	mux.HandleFunc("GET /api/mobile/status", s.handleMobileStatus)
	mux.HandleFunc("POST /api/mobile/start", s.handleMobileStart)
	mux.HandleFunc("POST /api/mobile/stop", s.handleMobileStop)
	mux.HandleFunc("POST /api/mobile/select-ip", s.handleMobileSelectIP)
	mux.HandleFunc("POST /api/mobile/sessions/revoke", s.handleMobileRevokeSession)
	mux.HandleFunc("POST /api/shutdown", s.handleShutdown)
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("POST /api/projects", s.handleAddProject)
	mux.HandleFunc("DELETE /api/projects/{id}", s.handleDeleteProject)
	mux.HandleFunc("POST /api/tasks", s.handleCreateTask)
	mux.HandleFunc("PATCH /api/tasks/{id}", s.handleRenameTask)
	mux.HandleFunc("POST /api/tasks/{id}/runs", s.handleAddTaskRuns)
	mux.HandleFunc("DELETE /api/tasks/{id}", s.handleDeleteTask)
	mux.HandleFunc("GET /api/tasks/{id}/diff", s.handleTaskDiff)
	mux.HandleFunc("POST /api/tasks/{id}/adopt", s.handleAdoptTask)
	mux.HandleFunc("DELETE /api/runs/{id}", s.handleDeleteRun)
	mux.HandleFunc("POST /api/runs/{id}/ack", s.handleAckRun)
	mux.HandleFunc("POST /api/runs/{id}/stop", s.handleStopRun)
	mux.HandleFunc("POST /api/runs/{id}/restart", s.handleRestartRun)
	mux.HandleFunc("GET /api/runs/{id}/sessions", s.handleRunSessions)
	mux.HandleFunc("PATCH /api/runs/{id}/session", s.handleBindRunSession)
	mux.HandleFunc("POST /api/shell/ensure", s.handleEnsureShell)
	mux.HandleFunc("GET /api/runs/{id}/screen", s.handleRunScreen)
	mux.HandleFunc("POST /api/quota/probe", s.handleQuotaProbe)
	mux.HandleFunc("POST /api/voice/transcribe", s.handleVoiceTranscribe)
	mux.HandleFunc("POST /api/hooks", s.handleHookEvent)
	mux.HandleFunc("GET /ws/events", s.handleEventsWS)
	mux.HandleFunc("GET /ws/runs/{id}/pty", s.handleRunPTYWS)

	s.mobile = mobile.NewBridge(mux)
	s.mobile.SetVoiceEnabledFunc(func() bool {
		return strings.TrimSpace(s.store.Config().VoiceTranscribeCmd) != ""
	})
	s.mobile.SetOnChangeFunc(func() {
		s.broadcast(protocol.DaemonEvent{Type: "state_changed", At: time.Now()})
	})
	s.httpSrv = &http.Server{Handler: mux}

	errCh := make(chan error, 1)
	go func() {
		if serveErr := s.httpSrv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		s.Shutdown()
		return nil
	case serveErr := <-errCh:
		return serveErr
	}
}

func (s *Server) Shutdown() {
	s.shuttingDown.Store(true)
	s.shutdownOnce.Do(func() {
		if s.mobile != nil {
			s.mobile.Stop()
		}
		s.pty.StopAll()
		if s.httpSrv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = s.httpSrv.Shutdown(ctx)
		}
		if s.listener != nil {
			_ = s.listener.Close()
		}
		// Only remove the socket path if it still points to the exact inode we created.
		if s.sockInfo != nil {
			if cur, err := os.Stat(s.sockPath); err == nil && os.SameFile(cur, s.sockInfo) {
				_ = os.Remove(s.sockPath)
			}
		}
	})
}

func (s *Server) handleRunStatusChange(runID string, status protocol.RunStatus, detail string, exitCode *int) {
	run, ok := s.store.GetRun(runID)
	if !ok {
		return
	}
	effectiveDetail := run.StatusDetail
	if detail != "" {
		effectiveDetail = detail
	}
	// Ignore terminal bell (\x07) unless the agent was actively running; bells emitted while
	// idle (e.g. Backspace on an empty input box or startup warnings) are not approval prompts.
	if status == protocol.StatusWaitingInput && effectiveDetail == "terminal bell" && run.Status != protocol.StatusRunning {
		return
	}
	// Suppress transient "title:ready" emitted during the brief initial boot of a fresh
	// prompt launch (e.g. Cursor CLI emitting "Cursor Agent - ✅ Ready" before "⏳ Working").
	if status == protocol.StatusDone && effectiveDetail == "title:ready" &&
		run.Status == protocol.StatusRunning && run.StatusDetail == "started" && time.Since(run.StartedAt) < 4*time.Second {
		return
	}
	// Deduplicate identical state ticks (e.g. agy/cursor emitting repeated working/idle title updates).
	if run.Status == status && run.StatusDetail == effectiveDetail && exitCode == nil {
		return
	}
	now := time.Now()
	run.Status = status
	run.StatusDetail = effectiveDetail
	if exitCode != nil {
		run.ExitCode = exitCode
		// Fallback session ID capture from exit banner (e.g. "codex resume <uuid>" printed on Ctrl+C/exit).
		if run.SessionID == "" {
			if sess, ok := s.pty.Get(runID); ok {
				if sid := extractResumeSessionID(ansi.Strip(sess.SnapshotScreen().ANSI)); sid != "" {
					run.SessionID = sid
				}
			}
		}
	}
	run.UpdatedAt = now
	if status == protocol.StatusDone || status == protocol.StatusError || status == protocol.StatusStopped {
		run.FinishedAt = &now
		if s.quota != nil {
			s.quota.RefreshAsync()
		}
	} else if status == protocol.StatusRunning || status == protocol.StatusWaitingInput {
		run.FinishedAt = nil
	}
	_ = s.store.UpdateRun(run)

	s.broadcast(protocol.DaemonEvent{
		Type:  "run_status",
		RunID: runID,
		Run:   &run,
		Msg:   detail,
		At:    now,
	})
}

func (s *Server) broadcast(ev protocol.DaemonEvent) {
	s.evMu.Lock()
	defer s.evMu.Unlock()
	for ch := range s.evSubs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if s.shuttingDown.Load() {
		writeErr(w, http.StatusServiceUnavailable, errors.New("daemon shutting down"))
		return
	}
	writeJSON(w, http.StatusOK, HealthResponse{
		OK:             true,
		PID:            os.Getpid(),
		Sock:           s.sockPath,
		Version:        version.Short(),
		BinFingerprint: s.binFingerprint,
		ActiveRuns:     s.pty.ActiveCount(),
	})
}

func (s *Server) handleSyncEnv(w http.ResponseWriter, r *http.Request) {
	var proxyMap map[string]string
	var clientPath string
	if r.Body != nil {
		var raw json.RawMessage
		if json.NewDecoder(r.Body).Decode(&raw) == nil && len(raw) > 0 {
			var req protocol.EnvSyncRequest
			if json.Unmarshal(raw, &req) == nil && (len(req.Proxy) > 0 || req.Path != "") {
				proxyMap = req.Proxy
				clientPath = req.Path
			} else {
				// Backwards compatibility with plain map[string]string payload.
				_ = json.Unmarshal(raw, &proxyMap)
			}
		}
	}
	if clientPath != "" {
		MergeClientPath(clientPath)
	}
	cfg := s.store.Config()
	if cfg.Proxy.Mode == protocol.ProxyModeManual || cfg.Proxy.Mode == protocol.ProxyModeDirect {
		writeJSON(w, http.StatusOK, map[string]any{"synced": 0, "mode": cfg.Proxy.Mode})
		return
	}
	norm := NormalizeProxyEnv(proxyMap)
	if hasActiveProxyURL(norm) {
		ApplyProxyEnv(norm)
		SaveProxyEnv(s.store.HomeDir(), norm)
		writeJSON(w, http.StatusOK, map[string]any{"synced": len(norm)})
		return
	}
	// Client shell had no proxy vars; if daemon also has none, try restoring a live saved proxy.
	if !hasActiveProxyURL(CaptureProxyEnv()) {
		if saved := LoadProxyEnv(s.store.HomeDir()); hasActiveProxyURL(saved) && ProxyEndpointReachable(saved, 150*time.Millisecond) {
			ApplyProxyEnv(saved)
			writeJSON(w, http.StatusOK, map[string]any{"restored": len(saved)})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"synced": 0})
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	s.shuttingDown.Store(true)
	writeJSON(w, http.StatusOK, map[string]bool{"shutting_down": true})
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.Shutdown()
	}()
}

func (s *Server) activeQuotaAgents() []string {
	snap := s.store.Snapshot(s.registry.ListInfo())
	return s.activeQuotaAgentsFrom(snap.Agents, snap.Runs)
}

func (s *Server) activeQuotaAgentsFrom(agents []protocol.AgentInfo, runs []protocol.Run) []string {
	if len(runs) > 0 {
		opened := make(map[string]bool, len(runs))
		for _, r := range runs {
			if r.Agent != "" {
				opened[r.Agent] = true
			}
		}
		var out []string
		seen := make(map[string]bool, len(opened))
		for _, a := range agents {
			if opened[a.Name] {
				out = append(out, a.Name)
				seen[a.Name] = true
			}
		}
		for _, r := range runs {
			if r.Agent != "" && !seen[r.Agent] {
				out = append(out, r.Agent)
				seen[r.Agent] = true
			}
		}
		return out
	}
	var out []string
	for _, a := range agents {
		if a.Available {
			out = append(out, a.Name)
		}
	}
	return out
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	snap := s.store.Snapshot(s.registry.ListInfo())
	if s.quota != nil {
		snap.Quotas = s.quota.SnapshotFor(s.activeQuotaAgentsFrom(snap.Agents, snap.Runs))
	}
	snap.Proxy = s.store.Config().Proxy
	snap.EscapeKey = s.store.Config().EscapeKey
	snap.DaemonVersion = version.Short()
	snap.Magpie = adapter.MagpieStatus()
	if s.mobile != nil {
		snap.Mobile = s.mobile.Status()
	}
	for _, info := range s.pty.ListByPrefix("install-") {
		snap.Installs = append(snap.Installs, protocol.InstallSessionInfo{
			Name:      strings.TrimPrefix(info.ID, "install-"),
			SessionID: info.ID,
			Running:   info.Running,
		})
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) handleAddProject(w http.ResponseWriter, r *http.Request) {
	var req protocol.AddProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.RepoPath) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("repo_path is required"))
		return
	}
	info, err := worktree.InspectWorkspace(req.RepoPath)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	isMulti := false
	subCount := 0
	if !info.IsGitRepo {
		subs := worktree.InspectSubRepos(info.Root)
		if len(subs) > 0 {
			isMulti = true
			subCount = len(subs)
		}
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = filepath.Base(info.Root)
	}
	proj := protocol.Project{
		ID:            "proj-" + shortID(),
		Name:          name,
		RepoPath:      info.Root,
		IsGitRepo:     info.IsGitRepo,
		IsMultiRepo:   isMulti,
		SubRepoCount:  subCount,
		DefaultAgents: req.DefaultAgents,
		CreatedAt:     time.Now(),
	}
	saved, err := s.store.AddProject(proj)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.broadcast(protocol.DaemonEvent{Type: "state_changed", At: time.Now()})
	writeJSON(w, http.StatusCreated, saved)
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	proj, ok := s.store.GetProject(id)
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("project not found"))
		return
	}
	snap := s.store.Snapshot(nil)
	taskByID := make(map[string]protocol.Task, len(snap.Tasks))
	for _, t := range snap.Tasks {
		taskByID[t.ID] = t
	}
	cleanSession := wantsCleanSession(r)
	seenWT := make(map[string]bool)
	var dirsToCheck []string
	dirsToCheck = append(dirsToCheck, proj.RepoPath)
	for _, run := range snap.Runs {
		if run.ProjectID == id {
			_ = s.pty.Remove(run.ID)
			if cleanSession && run.SessionID != "" {
				_ = CleanCLISession(run.Agent, run.WorktreePath, run.SessionID, "")
			}
			dirsToCheck = append(dirsToCheck, run.WorktreePath)
			if run.Branch != "" && run.WorktreePath != proj.RepoPath && !seenWT[run.WorktreePath] {
				seenWT[run.WorktreePath] = true
				if tk, ok := taskByID[run.TaskID]; ok && len(tk.SubRepos) > 0 {
					_ = worktree.RemoveMultiRepo(tk.SubRepos, run.WorktreePath, run.Branch)
				} else {
					_ = worktree.Remove(proj.RepoPath, run.WorktreePath, run.Branch)
				}
			}
		}
	}
	if err := s.store.DeleteProject(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	for _, d := range dirsToCheck {
		s.cleanupUnusedShell(d)
	}
	s.broadcast(protocol.DaemonEvent{Type: "state_changed", At: time.Now()})
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var req protocol.CreateTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	proj, ok := s.store.GetProject(req.ProjectID)
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("project not found"))
		return
	}
	if len(req.Agents) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("at least one agent must be selected"))
		return
	}
	repoInfo, err := worktree.InspectWorkspace(proj.RepoPath)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var subRepos []protocol.SubRepoRef
	if !repoInfo.IsGitRepo {
		subRepos = worktree.InspectSubRepos(repoInfo.Root)
	}
	isMultiRepo := !repoInfo.IsGitRepo && len(subRepos) > 0
	supportsWT := repoInfo.IsGitRepo || isMultiRepo

	if repoInfo.IsGitRepo != proj.IsGitRepo || isMultiRepo != proj.IsMultiRepo || len(subRepos) != proj.SubRepoCount {
		proj.IsGitRepo = repoInfo.IsGitRepo
		proj.IsMultiRepo = isMultiRepo
		proj.SubRepoCount = len(subRepos)
		_ = s.store.UpdateProject(proj)
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "task-" + shortID()
	}
	taskID := "task-" + shortID()
	slug := slugify(title)
	mode := "single"
	if len(req.Agents) > 1 {
		mode = "compare"
	}
	permMode := req.PermMode
	if permMode == "" {
		permMode = protocol.PermDefault
	}

	workMode := req.WorkMode
	switch workMode {
	case "worktree":
		workMode = "isolated"
	case "shared", "isolated":
		if !supportsWT {
			writeErr(w, http.StatusBadRequest, errors.New("当前目录及其子目录均不含 Git 仓库，仅支持 local（直连原目录）模式"))
			return
		}
	case "local":
		// Explicitly local.
	default:
		if supportsWT {
			if len(req.Agents) > 1 {
				workMode = "isolated"
			} else {
				workMode = "shared"
			}
		} else {
			workMode = "local"
		}
	}

	baseBranch := ""
	baseCommit := ""
	var taskSubRepos []protocol.SubRepoRef
	sharedWTPath := ""
	sharedBranch := ""
	cfg := s.store.Config()

	if workMode == "shared" || workMode == "isolated" {
		if repoInfo.IsGitRepo {
			baseBranch = repoInfo.Branch
			baseCommit = repoInfo.HeadCommit
		} else if isMultiRepo {
			taskSubRepos = subRepos
			baseBranch = fmt.Sprintf("multi(%d repos)", len(subRepos))
		}
	}
	if workMode == "shared" {
		sharedBranch = fmt.Sprintf("aio/%s-%s", slug, taskID[len(taskID)-4:])
		sharedWTPath = filepath.Join(cfg.WorktreeRoot, slugify(proj.Name), fmt.Sprintf("%s-%s", slug, taskID[len(taskID)-4:]))
		var wtErr error
		if len(taskSubRepos) > 0 {
			wtErr = worktree.CreateMultiRepo(proj.RepoPath, taskSubRepos, sharedWTPath, sharedBranch)
		} else {
			wtErr = worktree.Create(proj.RepoPath, repoInfo.HeadCommit, sharedWTPath, sharedBranch)
		}
		if wtErr != nil {
			writeErr(w, http.StatusInternalServerError, fmt.Errorf("create shared worktree: %w", wtErr))
			return
		}
	}

	task := protocol.Task{
		ID:           taskID,
		ProjectID:    proj.ID,
		Title:        title,
		Mode:         mode,
		WorkMode:     workMode,
		WorktreePath: sharedWTPath,
		Branch:       sharedBranch,
		BaseBranch:   baseBranch,
		BaseCommit:   baseCommit,
		SubRepos:     taskSubRepos,
		Prompt:       req.Prompt,
		CreatedAt:    time.Now(),
	}

	runs := make([]protocol.Run, 0, len(req.Agents))
	specs := make([]adapter.CommandSpec, 0, len(req.Agents))
	preSnaps := make([]map[string]struct{}, 0, len(req.Agents))

	removeWT := func(wtPath, branch string) {
		if len(taskSubRepos) > 0 {
			_ = worktree.RemoveMultiRepo(taskSubRepos, wtPath, branch)
		} else {
			_ = worktree.Remove(proj.RepoPath, wtPath, branch)
		}
	}

	for idx, agentName := range req.Agents {
		adp, err := s.registry.Get(agentName)
		if err != nil {
			s.cleanupPartialRuns(proj.RepoPath, taskSubRepos, runs)
			if sharedWTPath != "" {
				removeWT(sharedWTPath, sharedBranch)
			}
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		runID := fmt.Sprintf("run-%s-%s", agentName, shortID())

		wtPath := proj.RepoPath
		branchName := ""
		switch workMode {
		case "shared":
			wtPath = sharedWTPath
			branchName = sharedBranch
		case "isolated":
			suffix := agentName
			if idx > 0 {
				suffix = fmt.Sprintf("%s-%d", agentName, idx+1)
			}
			branchName = fmt.Sprintf("aio/%s-%s/%s", slug, taskID[len(taskID)-4:], suffix)
			wtPath = filepath.Join(cfg.WorktreeRoot, slugify(proj.Name), fmt.Sprintf("%s-%s-%s", slug, taskID[len(taskID)-4:], suffix))

			var wtErr error
			if len(taskSubRepos) > 0 {
				wtErr = worktree.CreateMultiRepo(proj.RepoPath, taskSubRepos, wtPath, branchName)
			} else {
				wtErr = worktree.Create(proj.RepoPath, repoInfo.HeadCommit, wtPath, branchName)
			}
			if wtErr != nil {
				s.cleanupPartialRuns(proj.RepoPath, taskSubRepos, runs)
				writeErr(w, http.StatusInternalServerError, fmt.Errorf("create worktree for %s: %w", agentName, wtErr))
				return
			}
		}

		initialSessionID := preAssignSessionID(agentName)
		preSnap := snapshotSessionIDs(agentName, wtPath, "")

		cmdSpec, err := adp.BuildLaunch(adapter.LaunchSpec{
			WorkDir:   wtPath,
			Prompt:    req.Prompt,
			Model:     req.Model,
			PermMode:  permMode,
			RunID:     runID,
			SockPath:  s.sockPath,
			SelfBin:   s.selfBin,
			SessionID: initialSessionID,
		})
		if err != nil {
			s.cleanupPartialRuns(proj.RepoPath, taskSubRepos, runs)
			if workMode == "isolated" && branchName != "" && wtPath != proj.RepoPath {
				removeWT(wtPath, branchName)
			}
			if sharedWTPath != "" {
				removeWT(sharedWTPath, sharedBranch)
			}
			writeErr(w, http.StatusInternalServerError, err)
			return
		}

		cmdSpec = s.prepareRunCommandSpec(agentName, req.MagpieModel, cmdSpec)

		now := time.Now()
		runs = append(runs, protocol.Run{
			ID:           runID,
			TaskID:       taskID,
			ProjectID:    proj.ID,
			Agent:        agentName,
			Model:        req.Model,
			MagpieModel:  strings.TrimSpace(req.MagpieModel),
			PermMode:     permMode,
			SessionID:    initialSessionID,
			WorktreePath: wtPath,
			Branch:       branchName,
			Status:       protocol.StatusStarting,
			StartedAt:    now,
			UpdatedAt:    now,
		})
		specs = append(specs, cmdSpec)
		preSnaps = append(preSnaps, preSnap)
	}

	if err := s.store.AddTask(task, runs); err != nil {
		s.cleanupPartialRuns(proj.RepoPath, taskSubRepos, runs)
		if sharedWTPath != "" {
			removeWT(sharedWTPath, sharedBranch)
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	// Spawn all selected agent PTYs concurrently.
	hasPrompt := strings.TrimSpace(req.Prompt) != ""
	for i := range runs {
		if _, err := s.pty.Start(runs[i].ID, specs[i]); err != nil {
			now := time.Now()
			runs[i].Status = protocol.StatusError
			runs[i].StatusDetail = err.Error()
			runs[i].UpdatedAt = now
			runs[i].FinishedAt = &now
			_ = s.store.UpdateRun(runs[i])
		} else {
			if hasPrompt {
				runs[i].Status = protocol.StatusRunning
				runs[i].StatusDetail = "started"
				_ = s.store.UpdateRun(runs[i])
			} else {
				// Launched interactively without an initial prompt -> idle/ready until user submits.
				runs[i].Status = protocol.StatusDone
				runs[i].StatusDetail = "ready"
				_ = s.store.UpdateRun(runs[i])
			}
			if runs[i].SessionID == "" {
				s.watchAndBindSession(runs[i].ID, runs[i].Agent, runs[i].WorktreePath, preSnaps[i])
			}
		}
	}

	s.broadcast(protocol.DaemonEvent{Type: "state_changed", At: time.Now()})
	writeJSON(w, http.StatusCreated, protocol.CreateTaskResponse{
		Task: task,
		Runs: runs,
	})
}

func (s *Server) cleanupPartialRuns(repoPath string, subRepos []protocol.SubRepoRef, runs []protocol.Run) {
	seen := make(map[string]bool)
	for _, r := range runs {
		if r.Branch != "" && r.WorktreePath != repoPath && !seen[r.WorktreePath] {
			seen[r.WorktreePath] = true
			if len(subRepos) > 0 {
				_ = worktree.RemoveMultiRepo(subRepos, r.WorktreePath, r.Branch)
			} else {
				_ = worktree.Remove(repoPath, r.WorktreePath, r.Branch)
			}
		}
	}
}

func (s *Server) handleRenameTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, ok := s.store.GetTask(id)
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("task not found"))
		return
	}
	var req protocol.RenameTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		writeErr(w, http.StatusBadRequest, errors.New("任务名称不能为空"))
		return
	}
	task.Title = title
	if err := s.store.UpdateTask(task); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.broadcast(protocol.DaemonEvent{Type: "state_changed", At: time.Now()})
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, ok := s.store.GetTask(id)
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("task not found"))
		return
	}
	proj, _ := s.store.GetProject(task.ProjectID)
	runs := s.store.RunsByTask(id)
	seenWT := make(map[string]bool)
	removeTaskWT := func(wtPath, branch string) {
		if len(task.SubRepos) > 0 {
			_ = worktree.RemoveMultiRepo(task.SubRepos, wtPath, branch)
		} else {
			_ = worktree.Remove(proj.RepoPath, wtPath, branch)
		}
	}
	cleanSession := wantsCleanSession(r)
	var dirsToCheck []string
	for _, run := range runs {
		_ = s.pty.Remove(run.ID)
		if cleanSession && run.SessionID != "" {
			_ = CleanCLISession(run.Agent, run.WorktreePath, run.SessionID, "")
		}
		dirsToCheck = append(dirsToCheck, run.WorktreePath)
		if proj.RepoPath != "" && run.Branch != "" && run.WorktreePath != proj.RepoPath && !seenWT[run.WorktreePath] {
			seenWT[run.WorktreePath] = true
			removeTaskWT(run.WorktreePath, run.Branch)
		}
	}
	if proj.RepoPath != "" && task.WorktreePath != "" && task.WorktreePath != proj.RepoPath && !seenWT[task.WorktreePath] {
		dirsToCheck = append(dirsToCheck, task.WorktreePath)
		removeTaskWT(task.WorktreePath, task.Branch)
	}
	if err := s.store.DeleteTask(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	for _, d := range dirsToCheck {
		s.cleanupUnusedShell(d)
	}
	s.broadcast(protocol.DaemonEvent{Type: "state_changed", At: time.Now()})
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) handleTaskDiff(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, ok := s.store.GetTask(id)
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("task not found"))
		return
	}
	runs := s.store.RunsByTask(id)
	summaries := make([]protocol.RunDiffSummary, 0, len(runs))
	for _, run := range runs {
		end := time.Now()
		if run.FinishedAt != nil {
			end = *run.FinishedAt
		}
		summary := protocol.RunDiffSummary{
			RunID:        run.ID,
			Agent:        run.Agent,
			Status:       run.Status,
			Branch:       run.Branch,
			WorktreePath: run.WorktreePath,
			ElapsedSec:   int64(end.Sub(run.StartedAt).Seconds()),
		}
		if len(task.SubRepos) > 0 && run.Branch != "" {
			files, ins, del, diffText, err := worktree.DiffMultiRepo(task.SubRepos, run.WorktreePath)
			if err != nil {
				summary.Error = err.Error()
			} else {
				summary.Files = files
				summary.FilesChanged = len(files)
				summary.Insertions = ins
				summary.Deletions = del
				summary.DiffText = diffText
			}
		} else if task.BaseCommit != "" {
			files, ins, del, diffText, err := worktree.DiffWorktree(run.WorktreePath, task.BaseCommit)
			if err != nil {
				summary.Error = err.Error()
			} else {
				summary.Files = files
				summary.FilesChanged = len(files)
				summary.Insertions = ins
				summary.Deletions = del
				summary.DiffText = diffText
			}
		} else {
			files, ins, del, diffText, repoCount, err := worktree.DiffLocalWorkspace(run.WorktreePath)
			if err != nil {
				summary.Error = err.Error()
			} else {
				summary.Files = files
				summary.FilesChanged = len(files)
				summary.Insertions = ins
				summary.Deletions = del
				summary.DiffText = diffText
				if repoCount == 0 {
					summary.Error = "该目录下未检测到任何 Git 仓库"
				}
			}
		}
		summaries = append(summaries, summary)
	}
	writeJSON(w, http.StatusOK, protocol.TaskDiffResponse{
		TaskID:     task.ID,
		BaseBranch: task.BaseBranch,
		BaseCommit: task.BaseCommit,
		Runs:       summaries,
	})
}

func (s *Server) handleAdoptTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, ok := s.store.GetTask(id)
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("task not found"))
		return
	}
	proj, ok := s.store.GetProject(task.ProjectID)
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("project not found"))
		return
	}
	if task.BaseCommit == "" && len(task.SubRepos) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("当前任务处于直连原目录模式（或无 Git 仓库），无需分支合并采纳"))
		return
	}
	var req protocol.AdoptTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	runs := s.store.RunsByTask(id)
	var winner *protocol.Run
	seenWT := make(map[string]bool)
	targets := make([]worktree.RunCleanupTarget, 0, len(runs))
	for i := range runs {
		if runs[i].ID == req.WinnerRunID {
			winner = &runs[i]
		}
		if runs[i].WorktreePath != "" && !seenWT[runs[i].WorktreePath] {
			seenWT[runs[i].WorktreePath] = true
			targets = append(targets, worktree.RunCleanupTarget{
				WorktreePath: runs[i].WorktreePath,
				Branch:       runs[i].Branch,
			})
		}
	}
	if winner == nil {
		writeErr(w, http.StatusBadRequest, errors.New("winner_run_id not found in task"))
		return
	}

	// Stop all running agent processes first so worktrees aren't actively written during merge/cleanup.
	for _, run := range runs {
		_ = s.pty.Stop(run.ID)
	}

	strategy := req.Strategy
	if strategy == "" {
		strategy = protocol.MergeSquash
	}
	commitMsg := strings.TrimSpace(req.CommitMsg)
	if commitMsg == "" {
		commitMsg = fmt.Sprintf("feat(%s): adopt %s result for %s", proj.Name, winner.Agent, task.Title)
	}

	var (
		cleaned      int
		warnings     []string
		adoptErr     error
		targetBranch = task.BaseBranch
	)
	if len(task.SubRepos) > 0 {
		var mergedRepos []string
		cleaned, mergedRepos, warnings, adoptErr = worktree.AdoptMultiRepo(
			task.SubRepos,
			winner.WorktreePath,
			winner.Branch,
			strategy,
			commitMsg,
			targets,
		)
		if len(mergedRepos) > 0 {
			targetBranch = strings.Join(mergedRepos, ", ")
		} else {
			targetBranch = "无改动子仓"
		}
	} else {
		cleaned, warnings, adoptErr = worktree.Adopt(
			proj.RepoPath,
			task.BaseBranch,
			winner.WorktreePath,
			winner.Branch,
			strategy,
			commitMsg,
			targets,
		)
	}
	if adoptErr != nil {
		writeErr(w, http.StatusConflict, adoptErr)
		return
	}

	task.WinnerRunID = winner.ID
	task.Archived = true
	_ = s.store.UpdateTask(task)

	// Delete task record after successful adoption + cleanup so sidebar stays focused on active work.
	_ = s.store.DeleteTask(task.ID)
	s.broadcast(protocol.DaemonEvent{Type: "state_changed", Msg: "adopted " + winner.Agent, At: time.Now()})

	writeJSON(w, http.StatusOK, protocol.AdoptTaskResponse{
		MergedBranch:     winner.Branch,
		TargetBranch:     targetBranch,
		Strategy:         string(strategy),
		CleanedWorktrees: cleaned,
		Warnings:         warnings,
	})
}

func (s *Server) handleAddTaskRuns(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	task, ok := s.store.GetTask(taskID)
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("task not found"))
		return
	}
	proj, ok := s.store.GetProject(task.ProjectID)
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("project not found"))
		return
	}
	var req protocol.AddRunsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(req.Agents) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("at least one agent must be selected"))
		return
	}

	permMode := req.PermMode
	if permMode == "" {
		permMode = protocol.PermDefault
	}
	slug := slugify(task.Title)
	cfg := s.store.Config()
	existingCount := len(s.store.RunsByTask(taskID))

	runs := make([]protocol.Run, 0, len(req.Agents))
	specs := make([]adapter.CommandSpec, 0, len(req.Agents))
	preSnaps := make([]map[string]struct{}, 0, len(req.Agents))

	for idx, agentName := range req.Agents {
		adp, err := s.registry.Get(agentName)
		if err != nil {
			s.cleanupPartialRuns(proj.RepoPath, task.SubRepos, runs)
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		runID := fmt.Sprintf("run-%s-%s", agentName, shortID())
		wtPath := proj.RepoPath
		branchName := ""
		if task.WorkMode == "shared" && task.WorktreePath != "" {
			// Handoff mode: reuse the task's single shared worktree & branch seamlessly!
			wtPath = task.WorktreePath
			branchName = task.Branch
		} else if len(task.SubRepos) > 0 {
			// Isolated compare mode on multi-repo workspace: create a fresh composite worktree.
			suffix := fmt.Sprintf("%s-%d", agentName, existingCount+idx+1)
			branchName = fmt.Sprintf("aio/%s-%s/%s", slug, taskID[len(taskID)-4:], suffix)
			wtPath = filepath.Join(cfg.WorktreeRoot, slugify(proj.Name), fmt.Sprintf("%s-%s-%s", slug, taskID[len(taskID)-4:], suffix))
			if err := worktree.CreateMultiRepo(proj.RepoPath, task.SubRepos, wtPath, branchName); err != nil {
				s.cleanupPartialRuns(proj.RepoPath, task.SubRepos, runs)
				writeErr(w, http.StatusInternalServerError, fmt.Errorf("create multi-repo worktree for %s: %w", agentName, err))
				return
			}
		} else if task.BaseCommit != "" {
			// Isolated compare mode on single Git repo.
			suffix := fmt.Sprintf("%s-%d", agentName, existingCount+idx+1)
			branchName = fmt.Sprintf("aio/%s-%s/%s", slug, taskID[len(taskID)-4:], suffix)
			wtPath = filepath.Join(cfg.WorktreeRoot, slugify(proj.Name), fmt.Sprintf("%s-%s-%s", slug, taskID[len(taskID)-4:], suffix))
			if err := worktree.Create(proj.RepoPath, task.BaseCommit, wtPath, branchName); err != nil {
				s.cleanupPartialRuns(proj.RepoPath, task.SubRepos, runs)
				writeErr(w, http.StatusInternalServerError, fmt.Errorf("create worktree for %s: %w", agentName, err))
				return
			}
		}

		initialSessionID := preAssignSessionID(agentName)
		preSnap := snapshotSessionIDs(agentName, wtPath, "")

		cmdSpec, err := adp.BuildLaunch(adapter.LaunchSpec{
			WorkDir:   wtPath,
			Prompt:    req.Prompt,
			Model:     req.Model,
			PermMode:  permMode,
			RunID:     runID,
			SockPath:  s.sockPath,
			SelfBin:   s.selfBin,
			SessionID: initialSessionID,
		})
		if err != nil {
			s.cleanupPartialRuns(proj.RepoPath, task.SubRepos, runs)
			if task.WorkMode != "shared" && branchName != "" && wtPath != proj.RepoPath {
				if len(task.SubRepos) > 0 {
					_ = worktree.RemoveMultiRepo(task.SubRepos, wtPath, branchName)
				} else {
					_ = worktree.Remove(proj.RepoPath, wtPath, branchName)
				}
			}
			writeErr(w, http.StatusInternalServerError, err)
			return
		}

		cmdSpec = s.prepareRunCommandSpec(agentName, req.MagpieModel, cmdSpec)

		now := time.Now()
		runs = append(runs, protocol.Run{
			ID:           runID,
			TaskID:       taskID,
			ProjectID:    proj.ID,
			Agent:        agentName,
			Model:        req.Model,
			MagpieModel:  strings.TrimSpace(req.MagpieModel),
			PermMode:     permMode,
			SessionID:    initialSessionID,
			WorktreePath: wtPath,
			Branch:       branchName,
			Status:       protocol.StatusStarting,
			StartedAt:    now,
			UpdatedAt:    now,
		})
		specs = append(specs, cmdSpec)
		preSnaps = append(preSnaps, preSnap)
	}

	if err := s.store.AddRuns(runs); err != nil {
		if task.WorkMode != "shared" {
			s.cleanupPartialRuns(proj.RepoPath, task.SubRepos, runs)
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	if task.WorkMode != "shared" && existingCount+len(runs) > 1 && task.Mode != "compare" {
		task.Mode = "compare"
		_ = s.store.UpdateTask(task)
	}

	hasPrompt := strings.TrimSpace(req.Prompt) != ""
	for i := range runs {
		if _, err := s.pty.Start(runs[i].ID, specs[i]); err != nil {
			now := time.Now()
			runs[i].Status = protocol.StatusError
			runs[i].StatusDetail = err.Error()
			runs[i].UpdatedAt = now
			runs[i].FinishedAt = &now
			_ = s.store.UpdateRun(runs[i])
		} else {
			if hasPrompt {
				runs[i].Status = protocol.StatusRunning
				runs[i].StatusDetail = "started"
				_ = s.store.UpdateRun(runs[i])
			} else {
				runs[i].Status = protocol.StatusDone
				runs[i].StatusDetail = "ready"
				_ = s.store.UpdateRun(runs[i])
			}
			if runs[i].SessionID == "" {
				s.watchAndBindSession(runs[i].ID, runs[i].Agent, runs[i].WorktreePath, preSnaps[i])
			}
		}
	}

	s.broadcast(protocol.DaemonEvent{Type: "state_changed", At: time.Now()})
	writeJSON(w, http.StatusCreated, runs)
}

func (s *Server) handleDeleteRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, ok := s.store.GetRun(id)
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("run not found"))
		return
	}
	proj, _ := s.store.GetProject(run.ProjectID)
	_ = s.pty.Remove(run.ID)
	if wantsCleanSession(r) && run.SessionID != "" {
		_ = CleanCLISession(run.Agent, run.WorktreePath, run.SessionID, "")
	}
	if err := s.store.DeleteRun(run.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.cleanupUnusedShell(run.WorktreePath)
	remaining := s.store.RunsByTask(run.TaskID)
	// Only remove the worktree & branch if no other remaining run shares the same worktree!
	sharedByOther := false
	for _, other := range remaining {
		if other.WorktreePath == run.WorktreePath {
			sharedByOther = true
			break
		}
	}
	if !sharedByOther && proj.RepoPath != "" && run.Branch != "" && run.WorktreePath != proj.RepoPath {
		if task, ok := s.store.GetTask(run.TaskID); ok && len(task.SubRepos) > 0 {
			_ = worktree.RemoveMultiRepo(task.SubRepos, run.WorktreePath, run.Branch)
		} else {
			_ = worktree.Remove(proj.RepoPath, run.WorktreePath, run.Branch)
		}
	}
	if len(remaining) == 0 {
		_ = s.store.DeleteTask(run.TaskID)
	} else if len(remaining) == 1 {
		if task, ok := s.store.GetTask(run.TaskID); ok && task.Mode == "compare" {
			task.Mode = "single"
			_ = s.store.UpdateTask(task)
		}
	}
	s.broadcast(protocol.DaemonEvent{Type: "state_changed", At: time.Now()})
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) handleAckRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, ok := s.store.GetRun(id)
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("run not found"))
		return
	}
	if run.Status == protocol.StatusWaitingInput {
		next := protocol.StatusRunning
		if sess, ok := s.pty.Get(id); !ok || sess.IsClosed() {
			next = protocol.StatusStopped
		} else if run.Agent == "cursor" && run.StatusDetail == "title:waiting_user" {
			// Cursor's "Waiting for you" means idle waiting for next user turn, not actively running.
			next = protocol.StatusDone
		}
		s.handleRunStatusChange(id, next, "acknowledged", nil)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleStopRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.store.GetRun(id); !ok {
		writeErr(w, http.StatusNotFound, errors.New("run not found"))
		return
	}
	_ = s.pty.Stop(id)
	writeJSON(w, http.StatusOK, map[string]bool{"stopped": true})
}

func (s *Server) handleRestartRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, ok := s.store.GetRun(id)
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("run not found"))
		return
	}
	var req protocol.RestartRunRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	resumeMode := strings.TrimSpace(req.Mode)
	sessionID := strings.TrimSpace(req.SessionID)
	if resumeMode == "" || resumeMode == "auto" {
		if run.SessionID != "" {
			resumeMode = "id"
			sessionID = run.SessionID
		} else {
			resumeMode = "last"
		}
	} else if resumeMode == "id" && sessionID == "" && run.SessionID != "" {
		sessionID = run.SessionID
	} else if resumeMode == "fresh" {
		sessionID = preAssignSessionID(run.Agent)
	}

	adp, err := s.registry.Get(run.Agent)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	_ = s.pty.Stop(id)
	preSnap := snapshotSessionIDs(run.Agent, run.WorktreePath, "")

	cmdSpec, err := adp.BuildResume(adapter.LaunchSpec{
		WorkDir:    run.WorktreePath,
		Model:      run.Model,
		PermMode:   run.PermMode,
		RunID:      run.ID,
		SockPath:   s.sockPath,
		SelfBin:    s.selfBin,
		ResumeMode: resumeMode,
		SessionID:  sessionID,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	cmdSpec = s.prepareRunCommandSpec(run.Agent, run.MagpieModel, cmdSpec)
	if _, err := s.pty.Start(run.ID, cmdSpec); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Resumed sessions open idle at the CLI prompt until the user submits a new turn.
	run.Status = protocol.StatusDone
	run.StatusDetail = "resume:" + resumeMode
	if resumeMode == "fresh" || resumeMode == "picker" {
		run.SessionID = sessionID
	} else if sessionID != "" {
		run.SessionID = sessionID
	}
	run.ExitCode = nil
	run.FinishedAt = nil
	run.UpdatedAt = time.Now()
	_ = s.store.UpdateRun(run)
	if run.SessionID == "" {
		s.watchAndBindSession(run.ID, run.Agent, run.WorktreePath, preSnap)
	}
	s.broadcast(protocol.DaemonEvent{Type: "run_status", RunID: run.ID, Run: &run, At: time.Now()})
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleRunSessions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, ok := s.store.GetRun(id)
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("run not found"))
		return
	}
	projDir := ""
	if proj, ok := s.store.GetProject(run.ProjectID); ok {
		projDir = proj.RepoPath
	}
	sessions := ListCLISessions(run.Agent, run.WorktreePath, projDir, "", s.store.HomeDir())
	if sessions == nil {
		sessions = []protocol.CLISessionInfo{}
	}
	writeJSON(w, http.StatusOK, sessions)
}

func (s *Server) handleBindRunSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, ok := s.store.GetRun(id)
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("run not found"))
		return
	}
	var req protocol.BindSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	run.SessionID = strings.TrimSpace(req.SessionID)
	run.UpdatedAt = time.Now()
	if err := s.store.UpdateRun(run); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.broadcast(protocol.DaemonEvent{Type: "run_status", RunID: run.ID, Run: &run, At: run.UpdatedAt})
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleEnsureShell(w http.ResponseWriter, r *http.Request) {
	var req protocol.EnsureShellRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	if req.Cols > 0 && req.Rows > 0 {
		s.pty.SetDefaultSize(req.Cols, req.Rows)
	}
	dir := cleanAbs(req.Dir)
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("working directory not found: %s", dir))
		return
	}

	shBin := strings.TrimSpace(os.Getenv("SHELL"))
	if shBin == "" {
		if p, err := exec.LookPath("bash"); err == nil {
			shBin = p
		} else {
			shBin = "/bin/sh"
		}
	}

	sum := md5.Sum([]byte(dir))
	shellID := "shell-" + hex.EncodeToString(sum[:5])

	if sess, ok := s.pty.Get(shellID); !ok || sess.IsClosed() {
		if _, err := s.pty.Start(shellID, adapter.CommandSpec{
			Binary: shBin,
			Dir:    dir,
			Env:    ProxyEnvOverridesForAgent("", s.store.Config().Proxy),
		}); err != nil {
			writeErr(w, http.StatusInternalServerError, fmt.Errorf("start shell: %w", err))
			return
		}
	}

	writeJSON(w, http.StatusOK, protocol.EnsureShellResponse{
		ShellID: shellID,
		Dir:     dir,
		Shell:   filepath.Base(shBin),
	})
}

func (s *Server) handleRunScreen(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := s.pty.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("no active pty session for run"))
		return
	}
	frame := sess.SnapshotScreen()
	writeJSON(w, http.StatusOK, protocol.PTYMessage{
		Type:   "screen",
		Data:   frame.ANSI,
		Cursor: &frame.Cursor,
	})
}

func (s *Server) handleQuotaProbe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunID string `json:"run_id,omitempty"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	if req.RunID != "" {
		if run, ok := s.store.GetRun(req.RunID); ok {
			if sess, ok := s.pty.Get(req.RunID); ok && !sess.IsClosed() {
				cmdText := "/usage\r"
				if run.Agent == "codex" {
					cmdText = "/status\r"
				}
				_ = sess.WriteInput([]byte(cmdText))
			}
		}
	}
	if s.quota != nil {
		s.quota.Refresh()
		if req.RunID == "" {
			go s.quota.ProbeAllSilent(context.Background())
		}
		writeJSON(w, http.StatusOK, s.quota.Snapshot())
		return
	}
	writeJSON(w, http.StatusOK, []protocol.AgentQuota{})
}

func (s *Server) handleHookEvent(w http.ResponseWriter, r *http.Request) {
	var req protocol.HookEventRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.RunID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("run_id required"))
		return
	}
	if sid := strings.TrimSpace(req.SessionID); sid != "" {
		if run, ok := s.store.GetRun(req.RunID); ok && run.SessionID != sid {
			run.SessionID = sid
			_ = s.store.UpdateRun(run)
		}
	}
	var st protocol.RunStatus
	switch strings.ToLower(req.Event) {
	case "waiting", "waiting_input", "notification":
		st = protocol.StatusWaitingInput
	case "done", "stop", "idle":
		st = protocol.StatusDone
	case "agy_idle":
		// Antigravity emits transient idle ticks during the first few seconds of a fresh `-i`
		// prompt launch before `working` begins. Suppress only that brief boot window; once
		// resumed, active, acknowledged, or past initial boot, `idle` always means done.
		run, ok := s.store.GetRun(req.RunID)
		if !ok || run.Status == protocol.StatusDone {
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}
		bootingWithPrompt := run.StatusDetail == "started" && time.Since(run.StartedAt) < 6*time.Second
		if bootingWithPrompt {
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}
		st = protocol.StatusDone
	case "running", "tool":
		st = protocol.StatusRunning
	case "error":
		st = protocol.StatusError
	default:
		st = protocol.StatusRunning
	}
	detail := req.Detail
	if detail == "" {
		detail = "hook:" + req.Event
	}
	s.handleRunStatusChange(req.RunID, st, detail, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleEventsWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ch := make(chan protocol.DaemonEvent, 32)
	s.evMu.Lock()
	s.evSubs[ch] = struct{}{}
	s.evMu.Unlock()
	defer func() {
		s.evMu.Lock()
		delete(s.evSubs, ch)
		s.evMu.Unlock()
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-done:
			return
		case ev := <-ch:
			if err := conn.WriteJSON(ev); err != nil {
				return
			}
		}
	}
}

func (s *Server) handleRunPTYWS(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := s.pty.Get(id)
	isInstall := strings.HasPrefix(id, "install-")
	if !ok || (sess.IsClosed() && !isInstall) {
		writeErr(w, http.StatusNotFound, errors.New("pty session not active"))
		return
	}
	rawMode := r.URL.Query().Get("mode") == "raw"
	fullMode := r.URL.Query().Get("full") == "1"

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	sub := sess.Subscribe(rawMode)
	defer sub.Close()

	// Send initial screen snapshot (TUI viewport, full mobile scrollback, or raw history).
	if rawMode {
		if hist := sess.RawHistory(); len(hist) > 0 {
			_ = conn.WriteJSON(protocol.PTYMessage{
				Type: "raw",
				Data: base64.StdEncoding.EncodeToString(hist),
			})
		}
	} else if fullMode {
		frame := sess.SnapshotFullScreen(800)
		_ = conn.WriteJSON(protocol.PTYMessage{
			Type:      "screen",
			Data:      frame.ANSI,
			ScrollMax: frame.ScrollMax,
			Cursor:    &frame.Cursor,
		})
	} else {
		frame := sess.SnapshotScreen()
		_ = conn.WriteJSON(protocol.PTYMessage{
			Type:         "screen",
			Data:         frame.ANSI,
			ScrollOffset: frame.ScrollOffset,
			ScrollMax:    frame.ScrollMax,
			Cursor:       &frame.Cursor,
			Links:        frame.Links,
		})
	}
	if sess.IsClosed() {
		_ = conn.WriteJSON(protocol.PTYMessage{Type: "closed"})
		return
	}

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		var inputBuf ptyInputTracker
		for {
			var msg protocol.PTYMessage
			if err := conn.ReadJSON(&msg); err != nil {
				return
			}
			switch msg.Type {
			case "input":
				raw, err := base64.StdEncoding.DecodeString(msg.Data)
				if err == nil && len(raw) > 0 {
					_ = sess.WriteInput(raw)
					submitted, hasEnter, isCancel, isEscapeSeq, isConfirmKey := inputBuf.Feed(raw)
					if run, ok := s.store.GetRun(id); ok {
						if run.Status == protocol.StatusWaitingInput {
							if isEscapeSeq {
								// Ignore pure escape sequences (arrows, focus in/out, mouse events).
							} else if isCancel || run.Agent == "cursor" {
								// Let Cursor's native OSC title ("⏳ Working") drive running state;
								// cancelling a prompt also returns cleanly to idle/done.
								if isCancel || hasEnter {
									s.handleRunStatusChange(id, protocol.StatusDone, "ready", nil)
								}
							} else if hasNativeRunningSignal(run.Agent) {
								// Claude & Antigravity emit native running hooks (UserPromptSubmit/PreToolUse/agy:working).
								// Only flip to running on explicit confirmation of an actionable approval prompt;
								// otherwise clear back to idle/done so typing never gets stuck in running.
								if hasEnter || isConfirmKey {
									if isActionableWaitingDetail(run.StatusDetail) {
										s.handleRunStatusChange(id, protocol.StatusRunning, "user confirm", nil)
									} else {
										s.handleRunStatusChange(id, protocol.StatusDone, "ready", nil)
									}
								}
							} else if hasEnter || isConfirmKey {
								s.handleRunStatusChange(id, protocol.StatusRunning, "user input", nil)
							}
						} else if run.Status == protocol.StatusDone && hasEnter && !hasNativeRunningSignal(run.Agent) {
							// For CLIs without native "turn started" hooks (e.g. Codex/Kimi/OpenCode),
							// flip idle -> running only when a real non-slash prompt was submitted.
							if isRealPromptSubmission(submitted) {
								s.handleRunStatusChange(id, protocol.StatusRunning, "user prompt", nil)
							}
						}
					}
				}
			case "paste":
				raw, err := base64.StdEncoding.DecodeString(msg.Data)
				if err == nil && len(raw) > 0 {
					_ = sess.Paste(string(raw))
					inputBuf.AppendPaste(string(raw))
					if run, ok := s.store.GetRun(id); ok && run.Status == protocol.StatusWaitingInput && !hasNativeRunningSignal(run.Agent) {
						s.handleRunStatusChange(id, protocol.StatusRunning, "user paste", nil)
					}
				}
			case "scroll":
				if msg.Delta == 0 {
					sess.ResetScroll()
				} else {
					sess.Scroll(msg.Delta, msg.X, msg.Y)
				}
			case "resize":
				if msg.Cols > 0 && msg.Rows > 0 {
					s.pty.SetDefaultSize(msg.Cols, msg.Rows)
					_ = sess.Resize(msg.Cols, msg.Rows)
				}
			}
		}
	}()

	for {
		if rawMode {
			select {
			case <-readDone:
				return
			case <-sess.Done():
				_ = conn.WriteJSON(protocol.PTYMessage{Type: "closed"})
				return
			case chunk := <-sub.Ch:
				if err := conn.WriteJSON(protocol.PTYMessage{
					Type: "raw",
					Data: base64.StdEncoding.EncodeToString(chunk),
				}); err != nil {
					return
				}
			}
		} else {
			select {
			case <-readDone:
				return
			case <-sess.Done():
				// Push the final screen snapshot before notifying closed so exit messages stay readable.
				var frame ptymgr.ScreenFrame
				if fullMode {
					frame = sess.SnapshotFullScreen(800)
				} else {
					frame = sess.SnapshotScreen()
				}
				_ = conn.WriteJSON(protocol.PTYMessage{
					Type:   "screen",
					Data:   frame.ANSI,
					Cursor: &frame.Cursor,
				})
				_ = conn.WriteJSON(protocol.PTYMessage{Type: "closed"})
				return
			case frame := <-sub.Screen:
				if fullMode {
					frame = sess.SnapshotFullScreen(800)
				}
				if err := conn.WriteJSON(protocol.PTYMessage{
					Type:         "screen",
					Data:         frame.ANSI,
					ScrollOffset: frame.ScrollOffset,
					ScrollMax:    frame.ScrollMax,
					Cursor:       &frame.Cursor,
					Links:        frame.Links,
				}); err != nil {
					return
				}
			}
		}
	}
}

// ptyInputTracker tracks the user's current unsent line in a PTY WebSocket session so
// empty Enter keystrokes or slash commands (/usage, /status, /model) do not falsely
// transition an idle agent into StatusRunning.
type ptyInputTracker struct {
	buf []rune
}

func (t *ptyInputTracker) AppendPaste(s string) {
	for _, r := range s {
		if r == '\r' || r == '\n' {
			continue
		}
		if r >= 0x20 {
			t.buf = append(t.buf, r)
		}
	}
	if len(t.buf) > 512 {
		t.buf = t.buf[len(t.buf)-512:]
	}
}

func (t *ptyInputTracker) Feed(raw []byte) (submitted string, hasEnter, isCancel, isEscapeSeq, isConfirmKey bool) {
	if bytes.Equal(raw, []byte{0x1b}) || bytes.Contains(raw, []byte{0x03}) {
		t.buf = nil
		return "", false, true, false, false
	}
	// Ignore pure ANSI escape sequences (arrow keys, focus in/out, mouse events, PgUp/PgDown, etc.).
	if len(raw) >= 2 && raw[0] == 0x1b {
		return "", false, false, true, false
	}
	wasEmpty := len(t.buf) == 0
	if wasEmpty && len(raw) == 1 {
		b := raw[0]
		if b == 'y' || b == 'Y' || b == 'n' || b == 'N' || (b >= '1' && b <= '9') {
			isConfirmKey = true
		}
	}
	for _, r := range string(raw) {
		switch r {
		case '\r', '\n':
			hasEnter = true
			submitted = string(t.buf)
			t.buf = nil
		case 0x7f, 0x08: // Backspace / Ctrl+H
			if len(t.buf) > 0 {
				t.buf = t.buf[:len(t.buf)-1]
			}
		case 0x15: // Ctrl+U (clear line)
			t.buf = nil
		default:
			if r >= 0x20 {
				t.buf = append(t.buf, r)
				if len(t.buf) > 512 {
					t.buf = t.buf[len(t.buf)-512:]
				}
			}
		}
	}
	return submitted, hasEnter, false, false, isConfirmKey
}

// hasNativeRunningSignal reports whether the agent CLI emits its own authoritative
// turn-start/turn-end lifecycle signals (hooks or OSC window titles) so PTY keystrokes
// should not speculatively force StatusRunning.
func hasNativeRunningSignal(agent string) bool {
	switch agent {
	case "claude", "agy", "cursor":
		return true
	default:
		return false
	}
}

func isActionableWaitingDetail(detail string) bool {
	d := strings.ToLower(strings.TrimSpace(detail))
	if d == "" || d == "terminal bell" {
		return false
	}
	return strings.Contains(d, "permission") ||
		strings.Contains(d, "confirm") ||
		strings.Contains(d, "elicitation") ||
		strings.Contains(d, "approval") ||
		strings.Contains(d, "reviewing") ||
		d == "hook:waiting"
}

func isRealPromptSubmission(line string) bool {
	s := strings.TrimSpace(line)
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "/") {
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func shortID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

var nonSlugRe = regexp.MustCompile(`[^a-z0-9]+`)
var resumeBannerSIDRe = regexp.MustCompile(`(?i)(?:codex\s+resume|claude\s+--resume|agy\s+--conversation|cursor-agent\s+--resume)\s+([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)
var codexWorkedForRe = regexp.MustCompile(`^Worked for \d+[hms]`)

// reconcileCodexStatusFromScreen provides a resilient fallback for Codex CLI turn completion.
// Codex's legacy `notify` hook passes the entire turn payload as a single argv argument, which
// fails with E2BIG ("Argument list too long") whenever the user pastes an image or large log.
// When that happens, detecting Codex's finished "Worked for ..." + idle "› " prompt ensures
// the sidebar reliably transitions from Running back to Done.
func (s *Server) reconcileCodexStatusFromScreen(run protocol.Run, plainScreen string) {
	if run.Agent != "codex" || run.Status != protocol.StatusRunning {
		return
	}
	if time.Since(run.UpdatedAt) < 3*time.Second {
		return
	}
	if detectCodexTurnDone(plainScreen) {
		s.handleRunStatusChange(run.ID, protocol.StatusDone, "screen:worked", nil)
	}
}

func detectCodexTurnDone(plainScreen string) bool {
	rawLines := strings.Split(plainScreen, "\n")
	nonEmpty := make([]string, 0, len(rawLines))
	for _, l := range rawLines {
		t := strings.TrimSpace(l)
		if t != "" {
			nonEmpty = append(nonEmpty, t)
		}
	}
	if len(nonEmpty) < 2 {
		return false
	}

	// Inspect the bottom window of the terminal viewport.
	tailStart := max(0, len(nonEmpty)-12)
	tail := nonEmpty[tailStart:]
	for _, l := range tail {
		low := strings.ToLower(l)
		if strings.Contains(low, "esc to interrupt") ||
			strings.Contains(low, "ctrl+c to interrupt") ||
			strings.Contains(low, "press enter to confirm") ||
			strings.Contains(low, "would you like to run") {
			return false
		}
	}

	// Find the last idle Codex input prompt line ("› ..." or bare "›") near the bottom
	// (allowing 1-2 footer/status lines underneath it).
	promptIdx := -1
	for i := len(tail) - 1; i >= max(0, len(tail)-4); i-- {
		if tail[i] == "›" || strings.HasPrefix(tail[i], "› ") {
			promptIdx = i
			break
		}
	}
	if promptIdx <= 0 {
		return false
	}

	// Verify a "Worked for <duration>" completion banner appears immediately above the idle
	// prompt (allowing at most 2 minor hint/tip lines, and NO new prompt/tool lines in between).
	for i := promptIdx - 1; i >= max(0, promptIdx-3); i-- {
		line := tail[i]
		if codexWorkedForRe.MatchString(line) {
			return true
		}
		if strings.HasPrefix(line, "›") || strings.HasPrefix(line, "• ") || strings.HasPrefix(line, "└") {
			return false
		}
	}
	return false
}

func extractResumeSessionID(plain string) string {
	if m := resumeBannerSIDRe.FindStringSubmatch(plain); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = nonSlugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "task"
	}
	if len(s) > 28 {
		s = s[:28]
		s = strings.TrimRight(s, "-")
	}
	return s
}
