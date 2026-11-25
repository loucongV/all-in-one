package daemon

import (
	"encoding/json"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/loucongV/all-in-one/internal/protocol"
)

var proxyEnvPairs = [][2]string{
	{"HTTP_PROXY", "http_proxy"},
	{"HTTPS_PROXY", "https_proxy"},
	{"ALL_PROXY", "all_proxy"},
	{"NO_PROXY", "no_proxy"},
}

var allowedProxyEnvKeys = map[string]bool{
	"HTTP_PROXY":  true,
	"http_proxy":  true,
	"HTTPS_PROXY": true,
	"https_proxy": true,
	"ALL_PROXY":   true,
	"all_proxy":   true,
	"NO_PROXY":    true,
	"no_proxy":    true,
}

var proxyEnvMu sync.Mutex

// CaptureProxyEnv collects active proxy environment variables from the current process
// and mirrors upper/lowercase pairs so all Agent CLIs (Go, Node, Rust, Python, curl)
// observe a consistent proxy configuration.
func CaptureProxyEnv() map[string]string {
	out := make(map[string]string)
	for _, pair := range proxyEnvPairs {
		u := strings.TrimSpace(os.Getenv(pair[0]))
		l := strings.TrimSpace(os.Getenv(pair[1]))
		if u == "" && l != "" {
			u = l
		} else if l == "" && u != "" {
			l = u
		}
		if u != "" {
			out[pair[0]] = u
		}
		if l != "" {
			out[pair[1]] = l
		}
	}
	return out
}

// NormalizeProxyEnv filters to allowed proxy keys and mirrors upper/lowercase pairs.
func NormalizeProxyEnv(in map[string]string) map[string]string {
	out := make(map[string]string)
	for _, pair := range proxyEnvPairs {
		u := strings.TrimSpace(in[pair[0]])
		l := strings.TrimSpace(in[pair[1]])
		if u == "" && l != "" {
			u = l
		} else if l == "" && u != "" {
			l = u
		}
		if u != "" {
			out[pair[0]] = u
		}
		if l != "" {
			out[pair[1]] = l
		}
	}
	return out
}

// ApplyProxyEnv writes normalized proxy variables into the current daemon process env
// so all newly started/restarted Agent PTYs, shells, and quota probes inherit them.
func ApplyProxyEnv(in map[string]string) map[string]string {
	norm := NormalizeProxyEnv(in)
	if len(norm) == 0 {
		return nil
	}
	proxyEnvMu.Lock()
	defer proxyEnvMu.Unlock()
	for k, v := range norm {
		if allowedProxyEnvKeys[k] && v != "" {
			_ = os.Setenv(k, v)
		}
	}
	return norm
}

func hasActiveProxyURL(env map[string]string) bool {
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		if strings.TrimSpace(env[k]) != "" {
			return true
		}
	}
	return false
}

func proxyEnvFilePath(homeDir string) string {
	return filepath.Join(homeDir, "proxy_env.json")
}

// SaveProxyEnv persists the latest non-empty proxy snapshot under <homeDir>/proxy_env.json.
func SaveProxyEnv(homeDir string, env map[string]string) {
	if homeDir == "" || !hasActiveProxyURL(env) {
		return
	}
	norm := NormalizeProxyEnv(env)
	b, err := json.MarshalIndent(norm, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(homeDir, 0o755)
	_ = os.WriteFile(proxyEnvFilePath(homeDir), b, 0o600)
}

// LoadProxyEnv loads a previously saved proxy snapshot from <homeDir>/proxy_env.json.
func LoadProxyEnv(homeDir string) map[string]string {
	if homeDir == "" {
		return nil
	}
	b, err := os.ReadFile(proxyEnvFilePath(homeDir))
	if err != nil {
		return nil
	}
	var raw map[string]string
	if json.Unmarshal(b, &raw) != nil {
		return nil
	}
	return NormalizeProxyEnv(raw)
}

// ProxyEndpointReachable checks whether the configured proxy host:port is actively listening
// (fast TCP dial) so aio never injects a stale dead proxy when the user has turned off Clash/VPN.
func ProxyEndpointReachable(env map[string]string, timeout time.Duration) bool {
	if timeout <= 0 {
		timeout = 150 * time.Millisecond
	}
	seen := make(map[string]bool)
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		raw := strings.TrimSpace(env[k])
		if raw == "" {
			continue
		}
		if !strings.Contains(raw, "://") {
			raw = "http://" + raw
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			continue
		}
		hostPort := u.Host
		if _, _, err := net.SplitHostPort(hostPort); err != nil {
			switch strings.ToLower(u.Scheme) {
			case "https":
				hostPort = net.JoinHostPort(hostPort, "443")
			case "socks5", "socks5h":
				hostPort = net.JoinHostPort(hostPort, "1080")
			default:
				hostPort = net.JoinHostPort(hostPort, "80")
			}
		}
		if seen[hostPort] {
			continue
		}
		seen[hostPort] = true
		conn, err := net.DialTimeout("tcp", hostPort, timeout)
		if err == nil {
			_ = conn.Close()
			return true
		}
	}
	return false
}

var unsetProxyOverrides = []string{
	"-HTTP_PROXY", "-http_proxy",
	"-HTTPS_PROXY", "-https_proxy",
	"-ALL_PROXY", "-all_proxy",
}

// ProxyEnvOverridesForAgent computes per-process environment variable overrides for an Agent
// or helper command based on the user's ProxyConfig (supporting auto, manual, direct, and
// per-agent direct-bypass routing such as keeping domestic agents direct while proxying others).
func ProxyEnvOverridesForAgent(agent string, cfg protocol.ProxyConfig) []string {
	mode := cfg.Mode
	if mode == "" {
		mode = protocol.ProxyModeAuto
	}
	if mode == protocol.ProxyModeDirect || (agent != "" && slices.Contains(cfg.DirectAgents, agent)) {
		return slices.Clone(unsetProxyOverrides)
	}
	if mode == protocol.ProxyModeManual {
		raw := map[string]string{
			"HTTP_PROXY":  cfg.HTTPProxy,
			"HTTPS_PROXY": cfg.HTTPSProxy,
			"ALL_PROXY":   cfg.AllProxy,
			"NO_PROXY":    cfg.NoProxy,
		}
		// Convenience: when user enters only HTTP_PROXY (typical Clash/V2Ray mixed port),
		// mirror it to HTTPS_PROXY so HTTPS requests also route through the proxy.
		if strings.TrimSpace(raw["HTTPS_PROXY"]) == "" && strings.TrimSpace(raw["HTTP_PROXY"]) != "" {
			raw["HTTPS_PROXY"] = raw["HTTP_PROXY"]
		}
		norm := NormalizeProxyEnv(raw)
		out := make([]string, 0, len(norm)+len(unsetProxyOverrides))
		for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
			if v, ok := norm[k]; ok && v != "" {
				out = append(out, k+"="+v)
			} else if k != "NO_PROXY" && k != "no_proxy" {
				out = append(out, "-"+k)
			}
		}
		return out
	}
	return nil
}

// TestProxyConnectivity dials the effective proxy endpoint (according to cfg) and measures latency.
func TestProxyConnectivity(cfg protocol.ProxyConfig, timeout time.Duration) protocol.ProxyTestResult {
	if timeout <= 0 {
		timeout = 1500 * time.Millisecond
	}
	if cfg.Mode == protocol.ProxyModeDirect {
		return protocol.ProxyTestResult{
			Reachable: true,
			Endpoint:  "direct (已禁用代理)",
		}
	}
	var env map[string]string
	if cfg.Mode == protocol.ProxyModeManual {
		raw := map[string]string{
			"HTTP_PROXY":  cfg.HTTPProxy,
			"HTTPS_PROXY": cfg.HTTPSProxy,
			"ALL_PROXY":   cfg.AllProxy,
		}
		if strings.TrimSpace(raw["HTTPS_PROXY"]) == "" && strings.TrimSpace(raw["HTTP_PROXY"]) != "" {
			raw["HTTPS_PROXY"] = raw["HTTP_PROXY"]
		}
		env = NormalizeProxyEnv(raw)
	} else {
		env = CaptureProxyEnv()
	}

	endpoint := firstProxyHostPort(env)
	if endpoint == "" {
		return protocol.ProxyTestResult{
			Reachable: false,
			Error:     "当前未配置任何代理地址",
		}
	}
	start := time.Now()
	conn, err := net.DialTimeout("tcp", endpoint, timeout)
	if err != nil {
		return protocol.ProxyTestResult{
			Reachable: false,
			Endpoint:  endpoint,
			Error:     err.Error(),
		}
	}
	_ = conn.Close()
	return protocol.ProxyTestResult{
		Reachable: true,
		Endpoint:  endpoint,
		LatencyMs: time.Since(start).Milliseconds(),
	}
}

func firstProxyHostPort(env map[string]string) string {
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		raw := strings.TrimSpace(env[k])
		if raw == "" {
			continue
		}
		if !strings.Contains(raw, "://") {
			raw = "http://" + raw
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			continue
		}
		hostPort := u.Host
		if _, _, err := net.SplitHostPort(hostPort); err != nil {
			switch strings.ToLower(u.Scheme) {
			case "https":
				hostPort = net.JoinHostPort(hostPort, "443")
			case "socks5", "socks5h":
				hostPort = net.JoinHostPort(hostPort, "1080")
			default:
				hostPort = net.JoinHostPort(hostPort, "80")
			}
		}
		return hostPort
	}
	return ""
}

// MergeClientPath prepends any missing directories from a client shell's PATH into the daemon's PATH
// so CLIs installed via nvm/fnm/mise/homebrew/local-bin are immediately discoverable.
func MergeClientPath(clientPath string) {
	clientPath = strings.TrimSpace(clientPath)
	if clientPath == "" {
		return
	}
	proxyEnvMu.Lock()
	defer proxyEnvMu.Unlock()
	cur := os.Getenv("PATH")
	existing := make(map[string]bool)
	var merged []string
	addDir := func(d string) {
		d = strings.TrimSpace(d)
		if d == "" || existing[d] {
			return
		}
		existing[d] = true
		merged = append(merged, d)
	}
	for _, d := range filepath.SplitList(cur) {
		addDir(d)
	}
	for _, d := range filepath.SplitList(clientPath) {
		addDir(d)
	}
	_ = os.Setenv("PATH", strings.Join(merged, string(os.PathListSeparator)))
}
