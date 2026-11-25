package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/loucongV/all-in-one/internal/protocol"
)

type Config struct {
	DefaultAgents      []string             `toml:"default_agents"`
	WorktreeRoot       string               `toml:"worktree_root"`
	EscapeKey          string               `toml:"escape_key"`
	DefaultMerge       string               `toml:"default_merge"`
	QuotaCommands      map[string]string    `toml:"quota_commands"`
	Proxy              protocol.ProxyConfig `toml:"proxy"`
	AgentBinaries      map[string]string    `toml:"agent_binaries"`
	MagpieBinary       string               `toml:"magpie_binary"`
	MobilePort         int                  `toml:"mobile_port"`
	VoiceTranscribeCmd string               `toml:"voice_transcribe_cmd"`
}

type stateFile struct {
	Projects []protocol.Project `json:"projects"`
	Tasks    []protocol.Task    `json:"tasks"`
	Runs     []protocol.Run     `json:"runs"`
}

type Store struct {
	mu       sync.RWMutex
	homeDir  string
	statePth string
	cfgPath  string
	cfg      Config
	state    stateFile
}

// HomeDir returns ~/.aio or $AIO_HOME if set.
func HomeDir() (string, error) {
	if env := os.Getenv("AIO_HOME"); env != "" {
		return env, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".aio"), nil
}

func SocketPath(homeDir string) string {
	return filepath.Join(homeDir, "daemon.sock")
}

func Open(homeDir string) (*Store, error) {
	if homeDir == "" {
		var err error
		homeDir, err = HomeDir()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		return nil, fmt.Errorf("create aio home: %w", err)
	}

	s := &Store{
		homeDir:  homeDir,
		statePth: filepath.Join(homeDir, "state.json"),
		cfgPath:  filepath.Join(homeDir, "config.toml"),
		cfg: Config{
			DefaultAgents: []string{"claude", "codex", "agy", "cursor", "kimi", "opencode"},
			WorktreeRoot:  filepath.Join(homeDir, "worktrees"),
			EscapeKey:     "ctrl+]",
			DefaultMerge:  string(protocol.MergeSquash),
			Proxy: protocol.ProxyConfig{
				Mode: protocol.ProxyModeAuto,
			},
			AgentBinaries: make(map[string]string),
		},
	}

	if err := s.loadConfig(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.cfg.WorktreeRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create worktree root: %w", err)
	}
	if err := s.loadState(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) HomeDir() string {
	return s.homeDir
}

func (s *Store) Config() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfg := s.cfg
	cfg.DefaultAgents = slices.Clone(s.cfg.DefaultAgents)
	cfg.Proxy.DirectAgents = slices.Clone(s.cfg.Proxy.DirectAgents)
	if s.cfg.QuotaCommands != nil {
		cfg.QuotaCommands = make(map[string]string, len(s.cfg.QuotaCommands))
		for k, v := range s.cfg.QuotaCommands {
			cfg.QuotaCommands[k] = v
		}
	}
	if s.cfg.AgentBinaries != nil {
		cfg.AgentBinaries = make(map[string]string, len(s.cfg.AgentBinaries))
		for k, v := range s.cfg.AgentBinaries {
			cfg.AgentBinaries[k] = v
		}
	}
	return cfg
}

// UpdateProxyConfig persists updated proxy settings atomically to config.toml.
func (s *Store) UpdateProxyConfig(proxy protocol.ProxyConfig) (protocol.ProxyConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if proxy.Mode == "" {
		proxy.Mode = protocol.ProxyModeAuto
	}
	proxy.HTTPProxy = strings.TrimSpace(proxy.HTTPProxy)
	proxy.HTTPSProxy = strings.TrimSpace(proxy.HTTPSProxy)
	proxy.AllProxy = strings.TrimSpace(proxy.AllProxy)
	proxy.NoProxy = strings.TrimSpace(proxy.NoProxy)
	proxy.DirectAgents = slices.Clone(proxy.DirectAgents)
	s.cfg.Proxy = proxy
	if err := s.saveConfigLocked(); err != nil {
		return protocol.ProxyConfig{}, err
	}
	return proxy, nil
}

// SetAgentBinary sets or clears a custom binary path for a named agent (or "magpie").
func (s *Store) SetAgentBinary(name, binPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	name = strings.TrimSpace(name)
	binPath = strings.TrimSpace(binPath)
	if name == "magpie" {
		s.cfg.MagpieBinary = binPath
		return s.saveConfigLocked()
	}
	if s.cfg.AgentBinaries == nil {
		s.cfg.AgentBinaries = make(map[string]string)
	}
	if binPath == "" {
		delete(s.cfg.AgentBinaries, name)
	} else {
		s.cfg.AgentBinaries[name] = binPath
	}
	return s.saveConfigLocked()
}

func (s *Store) saveConfigLocked() error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(s.cfg); err != nil {
		return err
	}
	tmp := s.cfgPath + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.cfgPath)
}

func (s *Store) loadConfig() error {
	data, err := os.ReadFile(s.cfgPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := toml.Decode(string(data), &s.cfg); err != nil {
		return err
	}
	if s.cfg.Proxy.Mode == "" {
		s.cfg.Proxy.Mode = protocol.ProxyModeAuto
	}
	if s.cfg.AgentBinaries == nil {
		s.cfg.AgentBinaries = make(map[string]string)
	}
	return nil
}

func (s *Store) loadState() error {
	data, err := os.ReadFile(s.statePth)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, &s.state)
}

func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.statePth + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.statePth)
}

func (s *Store) Snapshot(agents []protocol.AgentInfo) protocol.StateSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return protocol.StateSnapshot{
		Projects: slices.Clone(s.state.Projects),
		Tasks:    slices.Clone(s.state.Tasks),
		Runs:     slices.Clone(s.state.Runs),
		Agents:   agents,
	}
}

func (s *Store) AddProject(p protocol.Project) (protocol.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.state.Projects {
		if existing.RepoPath == p.RepoPath {
			return existing, nil
		}
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	s.state.Projects = append(s.state.Projects, p)
	return p, s.saveLocked()
}

func (s *Store) GetProject(id string) (protocol.Project, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.state.Projects {
		if p.ID == id {
			return p, true
		}
	}
	return protocol.Project{}, false
}

func (s *Store) UpdateProject(p protocol.Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Projects {
		if s.state.Projects[i].ID == p.ID {
			s.state.Projects[i] = p
			return s.saveLocked()
		}
	}
	return fmt.Errorf("project not found: %s", p.ID)
}

func (s *Store) DeleteProject(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Projects = slices.DeleteFunc(s.state.Projects, func(p protocol.Project) bool {
		return p.ID == id
	})
	s.state.Tasks = slices.DeleteFunc(s.state.Tasks, func(t protocol.Task) bool {
		return t.ProjectID == id
	})
	s.state.Runs = slices.DeleteFunc(s.state.Runs, func(r protocol.Run) bool {
		return r.ProjectID == id
	})
	return s.saveLocked()
}

func (s *Store) AddTask(t protocol.Task, runs []protocol.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Tasks = append(s.state.Tasks, t)
	s.state.Runs = append(s.state.Runs, runs...)
	return s.saveLocked()
}

func (s *Store) AddRuns(runs []protocol.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Runs = append(s.state.Runs, runs...)
	return s.saveLocked()
}

func (s *Store) DeleteRun(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Runs = slices.DeleteFunc(s.state.Runs, func(r protocol.Run) bool {
		return r.ID == id
	})
	return s.saveLocked()
}

func (s *Store) GetTask(id string) (protocol.Task, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.state.Tasks {
		if t.ID == id {
			return t, true
		}
	}
	return protocol.Task{}, false
}

func (s *Store) UpdateTask(t protocol.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Tasks {
		if s.state.Tasks[i].ID == t.ID {
			s.state.Tasks[i] = t
			return s.saveLocked()
		}
	}
	return fmt.Errorf("task not found: %s", t.ID)
}

func (s *Store) DeleteTask(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Tasks = slices.DeleteFunc(s.state.Tasks, func(t protocol.Task) bool {
		return t.ID == id
	})
	s.state.Runs = slices.DeleteFunc(s.state.Runs, func(r protocol.Run) bool {
		return r.TaskID == id
	})
	return s.saveLocked()
}

func (s *Store) GetRun(id string) (protocol.Run, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.state.Runs {
		if r.ID == id {
			return r, true
		}
	}
	return protocol.Run{}, false
}

func (s *Store) RunsByTask(taskID string) []protocol.Run {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []protocol.Run
	for _, r := range s.state.Runs {
		if r.TaskID == taskID {
			out = append(out, r)
		}
	}
	return out
}

func (s *Store) UpdateRun(r protocol.Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Runs {
		if s.state.Runs[i].ID == r.ID {
			s.state.Runs[i] = r
			return s.saveLocked()
		}
	}
	return fmt.Errorf("run not found: %s", r.ID)
}

func (s *Store) MarkOrphanedRunsStopped() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	now := time.Now()
	for i := range s.state.Runs {
		st := s.state.Runs[i].Status
		if st == protocol.StatusRunning || st == protocol.StatusStarting || st == protocol.StatusWaitingInput {
			s.state.Runs[i].Status = protocol.StatusStopped
			s.state.Runs[i].StatusDetail = "daemon restarted"
			s.state.Runs[i].UpdatedAt = now
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.saveLocked()
}
