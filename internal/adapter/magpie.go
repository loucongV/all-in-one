package adapter

import (
	"strings"

	"github.com/loucongV/all-in-one/internal/protocol"
)

// ResolveMagpie locates the local `magpie` binary (yetone/magpie) via the shared 3-tier resolver.
func ResolveMagpie() (string, bool) {
	return defaultResolver.Resolve("magpie", "magpie")
}

// MagpieStatus returns availability, resolved path, custom override, and install hint for Magpie.
func MagpieStatus() protocol.MagpieInfo {
	p, ok := ResolveMagpie()
	return protocol.MagpieInfo{
		Available:  ok,
		Path:       p,
		CustomPath: defaultResolver.Override("magpie"),
		InstallCmd: DefaultInstallCommand("magpie"),
	}
}

// MagpieSupportsAgent reports whether Magpie natively wraps launching the given agent CLI.
func MagpieSupportsAgent(agent string) bool {
	switch agent {
	case "claude", "codex":
		return true
	default:
		return false
	}
}

// WrapWithMagpie wraps an agent CommandSpec to launch via Magpie (Plan A integration),
// e.g. `magpie claude deepseek/deepseek-v4-pro -- <agent-args...>`, preserving working
// directory and environment overrides (aio hooks, proxy settings, etc.).
func WrapWithMagpie(magpieBin, agentName, magpieModel string, spec CommandSpec) CommandSpec {
	model := strings.TrimSpace(magpieModel)
	if magpieBin == "" || model == "" || !MagpieSupportsAgent(agentName) {
		return spec
	}
	args := []string{agentName}
	if model != "default" {
		args = append(args, model)
	}
	if len(spec.Args) > 0 {
		args = append(args, "--")
		args = append(args, spec.Args...)
	}
	return CommandSpec{
		Binary: magpieBin,
		Args:   args,
		Env:    spec.Env,
		Dir:    spec.Dir,
	}
}
