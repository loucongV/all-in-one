package adapter

import (
	"fmt"

	"github.com/loucongV/all-in-one/internal/protocol"
)

type LaunchSpec struct {
	WorkDir    string
	Prompt     string
	Model      string
	PermMode   protocol.PermMode
	RunID      string
	SockPath   string
	SelfBin    string // path to aio binary for hook callbacks
	ResumeMode string // picker | last | fresh | id
	SessionID  string
}

type CommandSpec struct {
	Binary string
	Args   []string
	Env    []string
	Dir    string
}

type Adapter interface {
	Name() string
	Label() string
	Binary() string
	ResolveBinary() (string, bool)
	BuildLaunch(spec LaunchSpec) (CommandSpec, error)
	BuildResume(spec LaunchSpec) (CommandSpec, error)
}

type Registry struct {
	order    []string
	adapters map[string]Adapter
}

func DefaultRegistry() *Registry {
	r := &Registry{
		adapters: make(map[string]Adapter),
	}
	r.Register(&ClaudeAdapter{})
	r.Register(&CodexAdapter{})
	r.Register(&AgyAdapter{})
	r.Register(&CursorAdapter{})
	r.Register(&KimiAdapter{})
	r.Register(&OpenCodeAdapter{})
	return r
}

// SetBinaryOverrides applies user-configured custom binary paths from config.toml.
func SetBinaryOverrides(overrides map[string]string, magpieBin string) {
	merged := make(map[string]string, len(overrides)+1)
	for k, v := range overrides {
		merged[k] = v
	}
	if magpieBin != "" {
		merged["magpie"] = magpieBin
	}
	defaultResolver.SetOverrides(merged)
}

// SetBinaryOverride updates or clears a single agent (or "magpie") custom binary path.
func SetBinaryOverride(name, binPath string) {
	defaultResolver.SetOverride(name, binPath)
}

// SetExtraSearchDirs configures extra binary lookup directories (mainly for tests).
func SetExtraSearchDirs(dirs []string) {
	defaultResolver.SetExtraDirs(dirs)
}

func (r *Registry) Register(a Adapter) {
	if _, exists := r.adapters[a.Name()]; !exists {
		r.order = append(r.order, a.Name())
	}
	r.adapters[a.Name()] = a
}

func (r *Registry) Get(name string) (Adapter, error) {
	a, ok := r.adapters[name]
	if !ok {
		return nil, fmt.Errorf("unknown agent adapter: %s", name)
	}
	return a, nil
}

func (r *Registry) ListInfo() []protocol.AgentInfo {
	out := make([]protocol.AgentInfo, 0, len(r.order))
	for _, name := range r.order {
		a := r.adapters[name]
		pth, ok := a.ResolveBinary()
		out = append(out, protocol.AgentInfo{
			Name:           a.Name(),
			Label:          a.Label(),
			Binary:         a.Binary(),
			Path:           pth,
			CustomPath:     defaultResolver.Override(a.Name()),
			Available:      ok,
			InstallCmd:     DefaultInstallCommand(a.Name()),
			SupportsMagpie: MagpieSupportsAgent(a.Name()),
		})
	}
	return out
}

func baseEnv(spec LaunchSpec) []string {
	env := []string{
		"AIO_RUN_ID=" + spec.RunID,
	}
	if spec.SockPath != "" {
		env = append(env, "AIO_SOCK_PATH="+spec.SockPath)
	}
	return env
}

func resolveToolBinary(name string, candidates ...string) (string, bool) {
	return defaultResolver.Resolve(name, candidates...)
}

func lookBinary(candidates ...string) (string, bool) {
	if len(candidates) == 0 {
		return "", false
	}
	return defaultResolver.Resolve(candidates[0], candidates...)
}
