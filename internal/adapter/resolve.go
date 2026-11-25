package adapter

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// BinaryResolver locates CLI binaries across three tiers:
//  1. Explicit user override from ~/.aio/config.toml (agent_binaries / magpie_binary)
//  2. Active PATH (including client-synced login shell PATH)
//  3. Curated common installation directories (~/.local/bin, nvm, fnm, mise, volta, bun, cargo, homebrew, etc.)
type BinaryResolver struct {
	mu        sync.RWMutex
	overrides map[string]string
	extraDirs []string
}

var defaultResolver = NewBinaryResolver()

func NewBinaryResolver() *BinaryResolver {
	return &BinaryResolver{
		overrides: make(map[string]string),
	}
}

// SetOverrides replaces the user-configured custom binary paths (keyed by agent name or "magpie").
func (r *BinaryResolver) SetOverrides(m map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := make(map[string]string, len(m))
	for k, v := range m {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			next[k] = expandHomePath(trimmed)
		}
	}
	r.overrides = next
}

// SetOverride updates or clears a single tool's custom binary path.
func (r *BinaryResolver) SetOverride(name, binPath string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	binPath = strings.TrimSpace(binPath)
	if binPath == "" {
		delete(r.overrides, name)
		return
	}
	r.overrides[name] = expandHomePath(binPath)
}

// Override returns the configured custom path for a tool, if any.
func (r *BinaryResolver) Override(name string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.overrides[name]
}

// SetExtraDirs configures extra search directories (primarily used in tests or custom environments).
func (r *BinaryResolver) SetExtraDirs(dirs []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.extraDirs = append([]string(nil), dirs...)
}

// Resolve locates an executable binary for tool `name` trying custom override, PATH, and common dirs.
func (r *BinaryResolver) Resolve(name string, candidates ...string) (resolved string, ok bool) {
	r.mu.RLock()
	override := r.overrides[name]
	extraDirs := append([]string(nil), r.extraDirs...)
	r.mu.RUnlock()

	// 1. User-specified custom path takes strict priority.
	if override != "" {
		if isExecutableFile(override) {
			return override, true
		}
		// Allow pointing to a directory containing the binary name.
		if fi, err := os.Stat(override); err == nil && fi.IsDir() {
			for _, cand := range candidates {
				full := filepath.Join(override, cand)
				if isExecutableFile(full) {
					return full, true
				}
			}
		}
	}

	// 2. Standard PATH lookup.
	for _, cand := range candidates {
		if p, err := exec.LookPath(cand); err == nil && isExecutableFile(p) {
			return p, true
		}
	}

	// 3. Extra + curated common installation directories (handles SSH non-login shells, nvm, mise, etc.).
	searchDirs := append(extraDirs, commonInstallDirs()...)
	for _, dir := range searchDirs {
		for _, cand := range candidates {
			full := filepath.Join(dir, cand)
			if isExecutableFile(full) {
				return full, true
			}
		}
	}

	return "", false
}

func commonInstallDirs() []string {
	home, _ := os.UserHomeDir()
	var dirs []string
	if home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, ".kimi-code", "bin"),
			filepath.Join(home, ".kimi", "bin"),
			filepath.Join(home, ".opencode", "bin"),
			filepath.Join(home, ".claude", "local"),
			filepath.Join(home, ".claude", "bin"),
			filepath.Join(home, ".magpie", "bin"),
			filepath.Join(home, ".cursor", "bin"),
			filepath.Join(home, ".gemini", "antigravity-cli", "bin"),
			filepath.Join(home, ".npm-global", "bin"),
			filepath.Join(home, ".local", "share", "pnpm"),
			filepath.Join(home, ".volta", "bin"),
			filepath.Join(home, ".bun", "bin"),
			filepath.Join(home, ".cargo", "bin"),
			filepath.Join(home, ".local", "share", "mise", "shims"),
			filepath.Join(home, ".asdf", "shims"),
			filepath.Join(home, ".nix-profile", "bin"),
			filepath.Join(home, "go", "bin"),
		)
		// Scan version managers (nvm, fnm, mise node installs), newest version first.
		for _, pat := range []string{
			filepath.Join(home, ".nvm", "versions", "node", "*", "bin"),
			filepath.Join(home, ".fnm", "node-versions", "*", "installation", "bin"),
			filepath.Join(home, ".local", "share", "mise", "installs", "node", "*", "bin"),
		} {
			if matches, err := filepath.Glob(pat); err == nil && len(matches) > 0 {
				sort.Sort(sort.Reverse(sort.StringSlice(matches)))
				dirs = append(dirs, matches...)
			}
		}
	}
	dirs = append(dirs,
		"/opt/homebrew/bin",
		"/usr/local/bin",
		"/snap/bin",
	)
	return dirs
}

func expandHomePath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if p == "~" {
				return home
			}
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func isExecutableFile(p string) bool {
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		return false
	}
	return fi.Mode()&0o111 != 0
}

// DefaultInstallCommand returns the official one-line installation command for each supported CLI.
func DefaultInstallCommand(name string) string {
	switch name {
	case "claude":
		return "curl -fsSL https://claude.ai/install.sh | bash"
	case "codex":
		return "npm install -g @openai/codex"
	case "cursor":
		return "curl -fsSL https://cursor.com/install | bash"
	case "kimi":
		return "curl -fsSL https://code.kimi.com/kimi-code/install.sh | bash"
	case "opencode":
		return "curl -fsSL https://opencode.ai/install | bash"
	case "magpie":
		return "curl -fsSL https://usemagpie.ai/install.sh | sh -s -- --tui"
	case "agy":
		return ""
	default:
		return ""
	}
}
