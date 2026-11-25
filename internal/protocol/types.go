package protocol

import "time"

// RunStatus represents the live lifecycle state of an agent run.
type RunStatus string

const (
	StatusStarting     RunStatus = "starting"
	StatusRunning      RunStatus = "running"
	StatusWaitingInput RunStatus = "waiting_input"
	StatusDone         RunStatus = "done"
	StatusError        RunStatus = "error"
	StatusStopped      RunStatus = "stopped"
)

// MergeStrategy matches GitHub's three standard pull request merge methods.
type MergeStrategy string

const (
	MergeSquash MergeStrategy = "squash"
	MergeCommit MergeStrategy = "merge"
	MergeRebase MergeStrategy = "rebase"
)

// PermMode controls agent approval / autonomy preset.
type PermMode string

const (
	PermDefault PermMode = "default"
	PermAuto    PermMode = "auto"
	PermPlan    PermMode = "plan"
	PermYolo    PermMode = "yolo"
)

type Project struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	RepoPath      string    `json:"repo_path"`
	IsGitRepo     bool      `json:"is_git_repo"`
	IsMultiRepo   bool      `json:"is_multi_repo,omitempty"`
	SubRepoCount  int       `json:"sub_repo_count,omitempty"`
	DefaultAgents []string  `json:"default_agents,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

func (p Project) SupportsWorktree() bool {
	return p.IsGitRepo || p.IsMultiRepo
}

// SubRepoRef tracks one nested Git repository inside a multi-repo workspace (e.g. workspace/service-api).
type SubRepoRef struct {
	RelPath    string `json:"rel_path"`
	RepoPath   string `json:"repo_path"`
	BaseBranch string `json:"base_branch"`
	BaseCommit string `json:"base_commit"`
}

type Task struct {
	ID           string       `json:"id"`
	ProjectID    string       `json:"project_id"`
	Title        string       `json:"title"`
	Mode         string       `json:"mode"`                    // "single" | "compare"
	WorkMode     string       `json:"work_mode,omitempty"`     // "local" | "shared" | "isolated"
	WorktreePath string       `json:"worktree_path,omitempty"` // populated when WorkMode == "shared"
	Branch       string       `json:"branch,omitempty"`        // populated when WorkMode == "shared"
	BaseBranch   string       `json:"base_branch"`
	BaseCommit   string       `json:"base_commit"`
	SubRepos     []SubRepoRef `json:"sub_repos,omitempty"` // populated for multi-repo composite worktrees
	Prompt       string       `json:"prompt"`
	WinnerRunID  string       `json:"winner_run_id,omitempty"`
	Archived     bool         `json:"archived,omitempty"`
	CreatedAt    time.Time    `json:"created_at"`
}

type Run struct {
	ID           string     `json:"id"`
	TaskID       string     `json:"task_id"`
	ProjectID    string     `json:"project_id"`
	Agent        string     `json:"agent"` // codex | claude | agy | cursor | kimi | opencode
	Model        string     `json:"model,omitempty"`
	MagpieModel  string     `json:"magpie_model,omitempty"` // optional Magpie provider/model route
	PermMode     PermMode   `json:"perm_mode"`
	SessionID    string     `json:"session_id,omitempty"` // captured CLI conversation/session ID for exact resume
	WorktreePath string     `json:"worktree_path"`
	Branch       string     `json:"branch"`
	Status       RunStatus  `json:"status"`
	StatusDetail string     `json:"status_detail,omitempty"`
	ExitCode     *int       `json:"exit_code,omitempty"`
	StartedAt    time.Time  `json:"started_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
}

type AgentInfo struct {
	Name           string `json:"name"`
	Label          string `json:"label"`
	Binary         string `json:"binary"`
	Path           string `json:"path,omitempty"`
	CustomPath     string `json:"custom_path,omitempty"`
	Available      bool   `json:"available"`
	InstallCmd     string `json:"install_cmd,omitempty"`
	SupportsMagpie bool   `json:"supports_magpie,omitempty"`
}

// ProxyMode controls how outgoing network proxy environment variables are applied to Agent CLIs.
type ProxyMode string

const (
	ProxyModeAuto   ProxyMode = "auto"   // inherit from shell environment (with live-probe cache)
	ProxyModeManual ProxyMode = "manual" // explicit proxy URLs from aio config
	ProxyModeDirect ProxyMode = "direct" // disable proxy for all agents
)

// ProxyConfig stores user-configurable proxy settings and per-agent direct-bypass rules.
type ProxyConfig struct {
	Mode         ProxyMode `json:"mode" toml:"mode"`
	HTTPProxy    string    `json:"http_proxy,omitempty" toml:"http_proxy"`
	HTTPSProxy   string    `json:"https_proxy,omitempty" toml:"https_proxy"`
	AllProxy     string    `json:"all_proxy,omitempty" toml:"all_proxy"`
	NoProxy      string    `json:"no_proxy,omitempty" toml:"no_proxy"`
	DirectAgents []string  `json:"direct_agents,omitempty" toml:"direct_agents"` // agents that bypass proxy (e.g. kimi)
}

// ProxyTestResult reports the outcome of a live TCP connectivity check against the configured proxy.
type ProxyTestResult struct {
	Reachable bool   `json:"reachable"`
	Endpoint  string `json:"endpoint,omitempty"`
	LatencyMs int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

// MagpieInfo reports local Magpie (yetone/magpie) availability and custom path override.
type MagpieInfo struct {
	Available  bool   `json:"available"`
	Path       string `json:"path,omitempty"`
	CustomPath string `json:"custom_path,omitempty"`
	InstallCmd string `json:"install_cmd,omitempty"`
}

// MobileSession describes one authenticated phone/browser session connected to the LAN mobile bridge.
type MobileSession struct {
	ID          string    `json:"id"`
	Device      string    `json:"device"`
	RemoteIP    string    `json:"remote_ip"`
	CreatedAt   time.Time `json:"created_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
	ActiveConns int       `json:"active_conns"`
}

// MobileStatus describes the LAN mobile web bridge state, pairing QR URL, and active client sessions.
type MobileStatus struct {
	Running    bool            `json:"running"`
	HTTPS      bool            `json:"https,omitempty"`
	Port       int             `json:"port,omitempty"`
	Token      string          `json:"token,omitempty"`
	SelectedIP string          `json:"selected_ip,omitempty"`
	LANIPs     []string        `json:"lan_ips,omitempty"`
	URL        string          `json:"url,omitempty"`
	Sessions   []MobileSession `json:"sessions,omitempty"`
}

// AgentQuota summarizes real-time quota / usage / rate-limit state for one Agent CLI.
type AgentQuota struct {
	Agent        string     `json:"agent"`
	State        string     `json:"state"` // "ok" | "low" | "limited" | "unknown"
	RemainingPct *float64   `json:"remaining_pct,omitempty"`
	UsedPct      *float64   `json:"used_pct,omitempty"`
	Summary      string     `json:"summary"`
	Detail       string     `json:"detail,omitempty"`
	ResetAt      *time.Time `json:"reset_at,omitempty"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type StateSnapshot struct {
	Projects      []Project            `json:"projects"`
	Tasks         []Task               `json:"tasks"`
	Runs          []Run                `json:"runs"`
	Agents        []AgentInfo          `json:"agents"`
	Quotas        []AgentQuota         `json:"quotas,omitempty"`
	Proxy         ProxyConfig          `json:"proxy"`
	Magpie        MagpieInfo           `json:"magpie"`
	Mobile        MobileStatus         `json:"mobile"`
	Installs      []InstallSessionInfo `json:"installs,omitempty"`
	EscapeKey     string               `json:"escape_key,omitempty"`
	DaemonVersion string               `json:"daemon_version,omitempty"`
}

// InstallSessionInfo describes an active or recently finished CLI installer PTY session.
type InstallSessionInfo struct {
	Name      string `json:"name"`
	SessionID string `json:"session_id"`
	Running   bool   `json:"running"`
}

// --- API Requests / Responses ---

type AddProjectRequest struct {
	RepoPath      string   `json:"repo_path"`
	Name          string   `json:"name,omitempty"`
	DefaultAgents []string `json:"default_agents,omitempty"`
}

type CreateTaskRequest struct {
	ProjectID   string   `json:"project_id"`
	Title       string   `json:"title"`
	Prompt      string   `json:"prompt"`
	Agents      []string `json:"agents"`
	WorkMode    string   `json:"work_mode,omitempty"` // "local" | "shared" | "isolated" | "" (auto)
	PermMode    PermMode `json:"perm_mode,omitempty"`
	Model       string   `json:"model,omitempty"`
	MagpieModel string   `json:"magpie_model,omitempty"`
}

type CreateTaskResponse struct {
	Task Task  `json:"task"`
	Runs []Run `json:"runs"`
}

type AddRunsRequest struct {
	Agents      []string `json:"agents"`
	Prompt      string   `json:"prompt,omitempty"`
	PermMode    PermMode `json:"perm_mode,omitempty"`
	Model       string   `json:"model,omitempty"`
	MagpieModel string   `json:"magpie_model,omitempty"`
}

// SetAgentBinaryRequest configures a user-chosen binary path for an agent or "magpie".
// An empty Path resets the tool back to automatic detection.
type SetAgentBinaryRequest struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// InstallAgentRequest requests launching or reattaching to an interactive PTY installer for an agent or "magpie".
type InstallAgentRequest struct {
	Name       string `json:"name"`
	AttachOnly bool   `json:"attach_only,omitempty"`
}

// InstallAgentResponse returns the PTY session ID streaming live or finished installation output.
type InstallAgentResponse struct {
	SessionID  string `json:"session_id"`
	Name       string `json:"name"`
	Command    string `json:"command"`
	Running    bool   `json:"running"`
	Reattached bool   `json:"reattached,omitempty"`
}

// MobileStartRequest starts, switches HTTP/HTTPS, or rotates the LAN mobile web bridge.
type MobileStartRequest struct {
	Port        int  `json:"port,omitempty"`
	RotateToken bool `json:"rotate_token,omitempty"`
	HTTPS       bool `json:"https,omitempty"`
}

// MobileSelectIPRequest switches the advertised LAN IP for the QR pairing URL.
type MobileSelectIPRequest struct {
	IP string `json:"ip"`
}

// MobileRevokeSessionRequest revokes one mobile session (by SessionID) or all sessions (All=true).
type MobileRevokeSessionRequest struct {
	SessionID string `json:"session_id,omitempty"`
	All       bool   `json:"all,omitempty"`
}

// EnvSyncRequest carries the launching client shell's proxy vars and PATH to the daemon.
type EnvSyncRequest struct {
	Proxy map[string]string `json:"proxy,omitempty"`
	Path  string            `json:"path,omitempty"`
}

type RenameTaskRequest struct {
	Title string `json:"title"`
}

type AdoptTaskRequest struct {
	WinnerRunID string        `json:"winner_run_id"`
	Strategy    MergeStrategy `json:"strategy"`
	CommitMsg   string        `json:"commit_msg,omitempty"`
}

type AdoptTaskResponse struct {
	MergedBranch     string   `json:"merged_branch"`
	TargetBranch     string   `json:"target_branch"`
	Strategy         string   `json:"strategy"`
	CleanedWorktrees int      `json:"cleaned_worktrees"`
	Warnings         []string `json:"warnings,omitempty"`
}

type HookEventRequest struct {
	RunID     string `json:"run_id"`
	Event     string `json:"event"` // waiting | done | running | error
	Detail    string `json:"detail,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// RestartRunRequest controls how an agent run is restarted / switched to another session.
type RestartRunRequest struct {
	Mode      string `json:"mode,omitempty"`       // auto | picker | last | fresh | id
	SessionID string `json:"session_id,omitempty"` // used when Mode == "id"
}

// BindSessionRequest binds an existing CLI session ID to a run without restarting it.
type BindSessionRequest struct {
	SessionID string `json:"session_id"`
}

// CLISessionInfo describes an existing local conversation/session discovered for an Agent CLI.
type CLISessionInfo struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Workspace string    `json:"workspace,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	SameDir   bool      `json:"same_dir,omitempty"`
}

type EnsureShellRequest struct {
	Dir  string `json:"dir,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

type EnsureShellResponse struct {
	ShellID string `json:"shell_id"`
	Dir     string `json:"dir"`
	Shell   string `json:"shell"`
}

type FileDiffStat struct {
	Path      string `json:"path"`
	Added     int    `json:"added"`
	Deleted   int    `json:"deleted"`
	Untracked bool   `json:"untracked,omitempty"`
}

type RunDiffSummary struct {
	RunID        string         `json:"run_id"`
	Agent        string         `json:"agent"`
	Status       RunStatus      `json:"status"`
	Branch       string         `json:"branch"`
	WorktreePath string         `json:"worktree_path"`
	ElapsedSec   int64          `json:"elapsed_sec"`
	FilesChanged int            `json:"files_changed"`
	Insertions   int            `json:"insertions"`
	Deletions    int            `json:"deletions"`
	Files        []FileDiffStat `json:"files"`
	DiffText     string         `json:"diff_text"`
	Error        string         `json:"error,omitempty"`
}

type TaskDiffResponse struct {
	TaskID     string           `json:"task_id"`
	BaseBranch string           `json:"base_branch"`
	BaseCommit string           `json:"base_commit"`
	Runs       []RunDiffSummary `json:"runs"`
}

// --- WebSocket Messages ---

type DaemonEvent struct {
	Type  string    `json:"type"` // state_changed | run_status | notification
	RunID string    `json:"run_id,omitempty"`
	Run   *Run      `json:"run,omitempty"`
	Msg   string    `json:"msg,omitempty"`
	At    time.Time `json:"at"`
}

// PTYMessage is exchanged over /ws/runs/{id}/pty.
type PTYMessage struct {
	Type         string     `json:"type"` // screen | raw | input | paste | resize | scroll | closed
	Data         string     `json:"data,omitempty"`
	Cols         int        `json:"cols,omitempty"`
	Rows         int        `json:"rows,omitempty"`
	Delta        int        `json:"delta,omitempty"` // scroll lines (+up, -down, 0=bottom)
	X            int        `json:"x,omitempty"`     // relative col for mouse wheel forwarding
	Y            int        `json:"y,omitempty"`     // relative row for mouse wheel forwarding
	ScrollOffset int        `json:"scroll_offset,omitempty"`
	ScrollMax    int        `json:"scroll_max,omitempty"`
	Cursor       *CursorPos `json:"cursor,omitempty"`
	Links        []LinkSpan `json:"links,omitempty"`
}

// LinkSpan locates a clickable hyperlink (OSC 8 or plain http/https URL) in viewport cell coordinates.
type LinkSpan struct {
	Row    int    `json:"row"`
	StartX int    `json:"start_x"`
	EndX   int    `json:"end_x"`
	URL    string `json:"url"`
}

type CursorPos struct {
	X      int  `json:"x"`
	Y      int  `json:"y"`
	Hidden bool `json:"hidden"`
}
