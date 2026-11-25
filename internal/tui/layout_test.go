package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/loucongV/all-in-one/internal/protocol"
)

func TestResponsiveSmallScreenLayoutNeverOverflows(t *testing.T) {
	m := NewModel(nil, nil)
	m.snap = protocol.StateSnapshot{
		Projects: []protocol.Project{{ID: "p1", Name: "finance", RepoPath: "/tmp/finance", IsMultiRepo: true}},
		Tasks:    []protocol.Task{{ID: "t1", ProjectID: "p1", Title: "金融研发助手"}},
		Runs:     []protocol.Run{{ID: "r1", TaskID: "t1", ProjectID: "p1", Agent: "agy", Status: protocol.StatusRunning}},
		Quotas: []protocol.AgentQuota{
			{Agent: "codex", State: "ok", Summary: "81% 剩 4d0h"},
			{Agent: "agy", State: "ok", Summary: "50% 剩 141h29m"},
		},
	}
	m.rebuildTree()
	m.sel = 2 // select run r1
	// Simulate an oversized stale 140-column PTY screen containing wide Chinese text right after shrinking the window.
	longLine := "中文测试超长日志行 " + strings.Repeat("finance-backend-platform/src/main/java/", 4)
	m.ptyScreen = strings.Repeat(longLine+"\n", 50)

	cases := []struct {
		name string
		w, h int
		mode focusMode
	}{
		{"wide-nav", 150, 42, modeNav},
		{"laptop-nav", 92, 28, modeNav},
		{"small-nav", 76, 24, modeNav},
		{"small-term", 76, 24, modeTerm},
		{"narrow-nav", 60, 20, modeNav},
		{"narrow-term", 60, 20, modeTerm},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m.width = tc.w
			m.height = tc.h
			m.mode = tc.mode

			cols, rows := m.ptyDimensions()
			sidebarW := m.sidebarWidth()
			if sidebarW+cols+4 > tc.w && tc.w >= 24 {
				t.Fatalf("ptyDimensions cols=%d + sidebarW=%d exceeds width=%d", cols, sidebarW, tc.w)
			}
			if rows > tc.h-4 {
				t.Fatalf("ptyDimensions rows=%d exceeds height-4=%d", rows, tc.h-4)
			}

			out := m.View()
			lines := strings.Split(out, "\n")
			if len(lines) > tc.h {
				t.Fatalf("rendered height %d exceeds terminal height %d", len(lines), tc.h)
			}
			for i, l := range lines {
				if lw := ansi.StringWidth(l); lw > tc.w {
					t.Fatalf("line %d rendered width %d exceeds terminal width %d", i, lw, tc.w)
				}
			}
		})
	}
}

func TestLockedFooterPreventsVerticalJumpOnClickAndRefresh(t *testing.T) {
	m := NewModel(nil, nil)
	m.width = 140
	m.height = 40
	m.snap = protocol.StateSnapshot{
		Projects: []protocol.Project{{ID: "p1", Name: "erp", RepoPath: "/tmp/erp"}},
		Tasks:    []protocol.Task{{ID: "t1", ProjectID: "p1", Title: "aiops"}},
		Runs:     []protocol.Run{{ID: "r1", TaskID: "t1", ProjectID: "p1", Agent: "codex", Status: protocol.StatusDone}},
	}
	m.rebuildTree()
	m.selectByRunID("r1")
	m.ptyScreen = strings.Repeat("codex output line\n", 40)

	m.mode = modeNav
	navCols, navRows := m.ptyDimensions()
	navLines := len(strings.Split(m.View(), "\n"))

	m.mode = modeTerm
	termCols, termRows := m.ptyDimensions()
	termLines := len(strings.Split(m.View(), "\n"))

	if navCols != termCols || navRows != termRows {
		t.Fatalf("clicking NAV<->TERM changed PTY dimensions: nav=(%d,%d) term=(%d,%d)", navCols, navRows, termCols, termRows)
	}
	if navLines != m.height || termLines != m.height {
		t.Fatalf("expected locked height %d in both modes, got nav=%d term=%d", m.height, navLines, termLines)
	}

	// Simulate background refresh banners (loading, multi-line error, waiting notification):
	// height and PTY dimensions MUST stay strictly invariant.
	m.loading = true
	if c, r := m.ptyDimensions(); c != termCols || r != termRows {
		t.Fatalf("loading banner shifted PTY dims: (%d,%d)", c, r)
	}
	m.loading = false
	m.errMsg = "first line\nsecond line error"
	if got := len(strings.Split(m.View(), "\n")); got != m.height {
		t.Fatalf("multi-line errMsg overflowed height: got %d want %d", got, m.height)
	}
}

func TestOverviewDashboardResponsiveLayout(t *testing.T) {
	m := NewModel(nil, nil)
	m.snap = protocol.StateSnapshot{
		Projects: []protocol.Project{{ID: "p1", Name: "all-in-one", RepoPath: "/home/dev/all-in-one", IsGitRepo: true}},
		Tasks:    []protocol.Task{{ID: "t1", ProjectID: "p1", Title: "dashboard", Mode: "compare", WorkMode: "isolated"}},
		Runs: []protocol.Run{
			{ID: "r1", TaskID: "t1", ProjectID: "p1", Agent: "claude", Status: protocol.StatusRunning},
			{ID: "r2", TaskID: "t1", ProjectID: "p1", Agent: "codex", Status: protocol.StatusWaitingInput},
		},
		Agents: []protocol.AgentInfo{
			{Name: "claude", Label: "Claude Code", Binary: "claude", Path: "/usr/local/bin/claude", Available: true},
			{Name: "codex", Label: "Codex CLI", Binary: "codex", Path: "/usr/local/bin/codex", Available: true},
			{Name: "agy", Label: "Antigravity", Binary: "agy", Path: "/usr/local/bin/agy", Available: true},
			{Name: "cursor", Label: "Cursor CLI", Binary: "cursor-agent", Path: "/usr/local/bin/cursor-agent", Available: true},
			{Name: "kimi", Label: "Kimi Code", Binary: "kimi", Available: false},
			{Name: "opencode", Label: "OpenCode", Binary: "opencode", Available: false},
		},
		Magpie: protocol.MagpieInfo{Available: true, Path: "/usr/local/bin/magpie"},
		Mobile: protocol.MobileStatus{Running: true, SelectedIP: "192.168.1.20"},
	}
	m.rebuildTree()
	m.sel = 0 // select project node -> triggers Overview Dashboard with Project context card

	sizes := []struct{ w, h int }{{160, 42}, {110, 32}, {84, 26}, {60, 22}}
	for _, sz := range sizes {
		m.width = sz.w
		m.height = sz.h
		m.mode = modeNav
		out := m.View()
		lines := strings.Split(out, "\n")
		if len(lines) != sz.h {
			t.Fatalf("overview %dx%d rendered %d lines, want %d", sz.w, sz.h, len(lines), sz.h)
		}
		for i, l := range lines {
			if lw := ansi.StringWidth(l); lw > sz.w {
				t.Fatalf("overview %dx%d line %d width %d > %d", sz.w, sz.h, i, lw, sz.w)
			}
		}
	}
}
