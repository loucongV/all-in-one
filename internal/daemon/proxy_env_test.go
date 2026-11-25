package daemon

import (
	"net"
	"slices"
	"testing"
	"time"

	"github.com/loucongV/all-in-one/internal/protocol"
)

func TestNormalizeSaveLoadAndReachableProxyEnv(t *testing.T) {
	norm := NormalizeProxyEnv(map[string]string{
		"http_proxy":  "http://127.0.0.1:7897",
		"HTTPS_PROXY": "http://127.0.0.1:7897",
		"no_proxy":    "localhost,127.0.0.1",
		"PATH":        "/should/be/ignored",
	})
	if norm["HTTP_PROXY"] != "http://127.0.0.1:7897" || norm["https_proxy"] != "http://127.0.0.1:7897" || norm["NO_PROXY"] != "localhost,127.0.0.1" {
		t.Fatalf("expected mirrored upper/lower proxy vars, got: %+v", norm)
	}
	if _, leaked := norm["PATH"]; leaked {
		t.Fatalf("non-proxy env var should be filtered out")
	}

	tmp := t.TempDir()
	SaveProxyEnv(tmp, norm)
	loaded := LoadProxyEnv(tmp)
	if loaded["HTTP_PROXY"] != "http://127.0.0.1:7897" || loaded["no_proxy"] != "localhost,127.0.0.1" {
		t.Fatalf("Save/Load proxy env mismatch: %+v", loaded)
	}

	// Open a local TCP listener to simulate an active local proxy (e.g. Clash).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	liveEnv := map[string]string{"HTTP_PROXY": "http://" + addr}
	if !ProxyEndpointReachable(liveEnv, 200*time.Millisecond) {
		_ = ln.Close()
		t.Fatalf("expected live local proxy %s to be reachable", addr)
	}
	_ = ln.Close()
	// Use a reserved non-ephemeral port (127.0.0.1:1) to avoid Linux TCP ephemeral self-connect flakes.
	closedEnv := map[string]string{"HTTP_PROXY": "http://127.0.0.1:1"}
	if ProxyEndpointReachable(closedEnv, 150*time.Millisecond) {
		t.Fatalf("expected closed local proxy 127.0.0.1:1 to be unreachable")
	}
}

func TestProxyEnvOverridesPerAgentAndConnectivity(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	cfg := protocol.ProxyConfig{
		Mode:         protocol.ProxyModeManual,
		HTTPProxy:    "http://" + ln.Addr().String(),
		NoProxy:      "localhost,127.0.0.1",
		DirectAgents: []string{"kimi"},
	}

	// Kimi is in DirectAgents -> must explicitly unset proxy vars.
	kimiEnv := ProxyEnvOverridesForAgent("kimi", cfg)
	if !slices.Contains(kimiEnv, "-HTTP_PROXY") || !slices.Contains(kimiEnv, "-HTTPS_PROXY") {
		t.Fatalf("expected kimi direct bypass to unset proxy vars, got: %v", kimiEnv)
	}

	// Claude gets manual proxy with HTTP_PROXY mirrored to HTTPS_PROXY.
	claudeEnv := ProxyEnvOverridesForAgent("claude", cfg)
	wantHTTPS := "HTTPS_PROXY=http://" + ln.Addr().String()
	if !slices.Contains(claudeEnv, wantHTTPS) || !slices.Contains(claudeEnv, "no_proxy=localhost,127.0.0.1") {
		t.Fatalf("expected claude manual proxy env, got: %v", claudeEnv)
	}

	// Live connectivity test succeeds against local listener.
	res := TestProxyConnectivity(cfg, 300*time.Millisecond)
	if !res.Reachable || res.Endpoint != ln.Addr().String() {
		t.Fatalf("expected reachable proxy test result, got: %+v", res)
	}
}
