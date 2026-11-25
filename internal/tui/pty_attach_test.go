package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/loucongV/all-in-one/internal/protocol"
)

func TestSyncPTYAttachmentPreventsDuplicateConcurrentConnects(t *testing.T) {
	m := NewModel(nil, nil)
	m.snap = protocol.StateSnapshot{
		Projects: []protocol.Project{{ID: "p1", Name: "demo", RepoPath: "/tmp/demo"}},
		Tasks:    []protocol.Task{{ID: "t1", ProjectID: "p1", Title: "task"}},
		Runs:     []protocol.Run{{ID: "r1", TaskID: "t1", ProjectID: "p1", Agent: "codex"}},
	}
	m.rebuildTree()
	m.selectByRunID("r1")

	// First sync starts connecting and bumps ptyGen.
	cmd1 := m.syncPTYAttachment()
	if cmd1 == nil || !m.ptyConnecting || m.ptyGen != 1 {
		t.Fatalf("expected initial connect cmd with gen=1, got cmd=%v connecting=%v gen=%d", cmd1 != nil, m.ptyConnecting, m.ptyGen)
	}

	// Second sync while still connecting MUST return nil (no duplicate WS dial!).
	cmd2 := m.syncPTYAttachment()
	if cmd2 != nil || m.ptyGen != 1 {
		t.Fatalf("expected no duplicate connect while in-flight, got cmd=%v gen=%d", cmd2 != nil, m.ptyGen)
	}

	// Stale ptyScreenMsg with old generation MUST be ignored and return nil.
	updated, cmd3 := m.Update(ptyScreenMsg{runID: "r1", gen: 0, screen: "stale"})
	if cmd3 != nil {
		t.Fatalf("expected stale ptyScreenMsg to be ignored")
	}
	if updated.(Model).ptyScreen != "" {
		t.Fatalf("stale screen should not overwrite current screen")
	}
}

func TestPTYClosedStopsReconnectLoopAndExitsTerm(t *testing.T) {
	m := NewModel(nil, nil)
	m.width = 120
	m.height = 36
	m.snap = protocol.StateSnapshot{
		Projects: []protocol.Project{{ID: "p1", Name: "demo", RepoPath: "/tmp/demo"}},
		Tasks:    []protocol.Task{{ID: "t1", ProjectID: "p1", Title: "task"}},
		Runs:     []protocol.Run{{ID: "r1", TaskID: "t1", ProjectID: "p1", Agent: "codex"}},
	}
	m.rebuildTree()
	m.selectByRunID("r1")
	m.mode = modeTerm
	m.attachedRunID = "r1"
	m.ptyGen = 2
	m.ptyScreen = "To continue this session, run: codex resume 01a20791-26b3-7601-a058-d0c7c6833c41"

	// Simulate PTY exiting (e.g. user hit Ctrl+C inside Codex).
	nextModel, _ := m.Update(ptyClosedMsg{runID: "r1", gen: 2})
	m2 := nextModel.(Model)
	if !m2.ptyDead {
		t.Fatalf("expected ptyDead=true after ptyClosedMsg")
	}
	if m2.mode != modeNav {
		t.Fatalf("expected ptyClosedMsg to exit modeTerm to modeNav, got %v", m2.mode)
	}

	// Subsequent stateLoadedMsg MUST NOT re-dial the dead session (prevents infinite reconnect loop).
	nextModel, cmd := m2.Update(stateLoadedMsg{snap: m2.snap})
	m3 := nextModel.(Model)
	if cmd != nil || m3.ptyConnecting || m3.ptyGen != 2 {
		t.Fatalf("expected no reconnect loop on dead PTY, got cmd=%v connecting=%v gen=%d", cmd != nil, m3.ptyConnecting, m3.ptyGen)
	}
}

func TestSidebarCollapseExpandAndAutoUncollapse(t *testing.T) {
	m := NewModel(nil, nil)
	m.snap = protocol.StateSnapshot{
		Projects: []protocol.Project{{ID: "p1", Name: "stock", RepoPath: "/tmp/stock"}},
		Tasks:    []protocol.Task{{ID: "t1", ProjectID: "p1", Title: "aio测试"}},
		Runs: []protocol.Run{
			{ID: "r1", TaskID: "t1", ProjectID: "p1", Agent: "codex", Status: protocol.StatusRunning},
			{ID: "r2", TaskID: "t1", ProjectID: "p1", Agent: "claude", Status: protocol.StatusWaitingInput},
		},
	}
	m.rebuildTree()
	if len(m.nodes) != 4 { // p1, t1, r1, r2
		t.Fatalf("expected 4 expanded nodes, got %d", len(m.nodes))
	}

	// Collapse project p1 -> only 1 node left.
	m.sel = 0
	if !m.toggleCurrentCollapse() || len(m.nodes) != 1 {
		t.Fatalf("expected collapsed project to show 1 node, got %d", len(m.nodes))
	}

	// selectByRunID("r2") MUST auto-uncollapse p1 and t1!
	if !m.selectByRunID("r2") || len(m.nodes) != 4 || m.currentRunID() != "r2" {
		t.Fatalf("expected selectByRunID to auto-expand parents, nodes=%d cur=%s", len(m.nodes), m.currentRunID())
	}

	// Left/h on run r2 jumps to parent task t1, then second left/h collapses t1.
	m.collapseOrJumpParent()
	if m.nodes[m.sel].kind != nodeTask {
		t.Fatalf("expected jump to parent task, got %+v", m.nodes[m.sel])
	}
	m.collapseOrJumpParent()
	if len(m.nodes) != 2 { // p1, t1
		t.Fatalf("expected collapsed task to hide runs, got %d nodes", len(m.nodes))
	}
}

func TestF12TogglesNativeMouseModeAndAltMPassesThroughToAgent(t *testing.T) {
	m := NewModel(nil, nil)
	m.mode = modeTerm
	if m.mouseDisabled {
		t.Fatalf("expected mouse enabled by default")
	}
	// Alt+M MUST NOT be hijacked in TERM mode so Antigravity (agy) keeps its native Alt+M shortcut.
	afterAltM, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}, Alt: true})
	if afterAltM.(Model).mouseDisabled {
		t.Fatalf("expected Alt+M in TERM mode to pass through to agent without toggling mouse mode")
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyF12})
	m2 := next.(Model)
	if !m2.mouseDisabled || cmd == nil {
		t.Fatalf("expected F12 in TERM mode to disable mouse capture, mouseDisabled=%v cmd=%v", m2.mouseDisabled, cmd != nil)
	}
	next, cmd = m2.Update(tea.KeyMsg{Type: tea.KeyF12})
	m3 := next.(Model)
	if m3.mouseDisabled || cmd == nil {
		t.Fatalf("expected second F12 to re-enable mouse capture, mouseDisabled=%v cmd=%v", m3.mouseDisabled, cmd != nil)
	}
}

func TestInstallerClosedRetainsLogAndDismissesCleanly(t *testing.T) {
	m := NewModel(nil, nil)
	m.width = 110
	m.height = 24
	m.shellMode = true
	m.shellID = "install-magpie"
	m.shellDir = "install magpie"
	m.shellBin = "installer"
	m.attachedRunID = "install-magpie"
	m.ptyGen = 3
	m.mode = modeTerm

	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, fmt.Sprintf("step %02d: downloading magpie...", i))
	}
	lines = append(lines, "✓ magpie 安装完成 (exit=0)！", "", "   ")
	m.ptyScreen = strings.Join(lines, "\n")

	next, _ := m.Update(ptyClosedMsg{runID: "install-magpie", gen: 3})
	m2 := next.(Model)
	if !m2.shellMode || !m2.ptyDead || m2.mode != modeNav {
		t.Fatalf("expected finished installer to remain open in NAV mode, shellMode=%v ptyDead=%v mode=%v", m2.shellMode, m2.ptyDead, m2.mode)
	}

	rendered := ansi.Strip(m2.renderShellPane(80, 20))
	if !strings.Contains(rendered, "✓ magpie 安装完成 (exit=0)") {
		t.Fatalf("expected final installer result line to remain visible, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "安装已结束") || !strings.Contains(rendered, "Esc / Enter / t") {
		t.Fatalf("expected installer action bar to be rendered, got:\n%s", rendered)
	}

	// Pressing Esc dismisses the finished installer screen cleanly.
	afterEsc, _ := m2.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mEsc := afterEsc.(Model)
	if mEsc.shellMode || mEsc.ptyDead {
		t.Fatalf("expected Esc to dismiss installer screen, shellMode=%v ptyDead=%v", mEsc.shellMode, mEsc.ptyDead)
	}

	// Pressing Enter also dismisses the finished installer screen cleanly.
	afterEnter, _ := m2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mEnter := afterEnter.(Model)
	if mEnter.shellMode || mEnter.ptyDead {
		t.Fatalf("expected Enter to dismiss installer screen, shellMode=%v ptyDead=%v", mEnter.shellMode, mEnter.ptyDead)
	}
}

func TestBackgroundInstallIndicatorAndToolsModalAutoFocus(t *testing.T) {
	m := NewModel(nil, nil)
	m.width = 120
	m.height = 32
	m.snap = protocol.StateSnapshot{
		Agents: []protocol.AgentInfo{
			{Name: "claude", Label: "Claude Code", Available: true, Path: "/usr/local/bin/claude"},
			{Name: "kimi", Label: "Kimi Code", Available: false, InstallCmd: "curl -fsSL https://code.kimi.com/kimi-code/install.sh | bash"},
		},
		Magpie: protocol.MagpieInfo{
			Available:  false,
			InstallCmd: "curl -fsSL https://usemagpie.ai/install.sh | sh -s -- --tui",
		},
		Installs: []protocol.InstallSessionInfo{
			{Name: "magpie", SessionID: "install-magpie", Running: true},
		},
	}

	// While browsing another task in NAV mode, footer reminds how to jump back.
	footer := ansi.Strip(m.renderFooter(120))
	if !strings.Contains(footer, "magpie") || !strings.Contains(footer, "回到安装界面") {
		t.Fatalf("expected background install hint in footer, got: %q", footer)
	}

	// Opening Tools modal (I) auto-focuses the running installer row and labels it cleanly.
	m.openToolsModal()
	rows := m.toolRows()
	if m.toolsCursor != len(rows)-1 || rows[m.toolsCursor].name != "magpie" {
		t.Fatalf("expected openToolsModal to auto-focus magpie row, cursor=%d rows=%+v", m.toolsCursor, rows)
	}
	body := ansi.Strip(m.renderToolsModalBody())
	if !strings.Contains(body, "安装中") || !strings.Contains(body, "按 Enter 回到安装界面") {
		t.Fatalf("expected running installer status in tools modal, got:\n%s", body)
	}

	// Once reattached to the installer pane itself, footer stops nagging about background install.
	m.shellMode = true
	m.shellID = "install-magpie"
	if _, ok := m.backgroundRunningInstall(); ok {
		t.Fatalf("expected backgroundRunningInstall=false while already viewing install-magpie")
	}
}

func TestQuotaPanelShowsOnlyOpenedAgentsOrInstalledFallback(t *testing.T) {
	m := NewModel(nil, nil)
	m.snap = protocol.StateSnapshot{
		Agents: []protocol.AgentInfo{
			{Name: "claude", Label: "Claude Code", Available: true},
			{Name: "codex", Label: "Codex CLI", Available: true},
			{Name: "kimi", Label: "Kimi Code", Available: true},
			{Name: "opencode", Label: "OpenCode", Available: false},
		},
		Runs: []protocol.Run{
			{ID: "r1", Agent: "codex"},
			{ID: "r2", Agent: "kimi"},
		},
		Quotas: []protocol.AgentQuota{
			{Agent: "claude", State: "ok", Summary: "90% 剩"},
			{Agent: "codex", State: "ok", Summary: "80% 剩"},
			{Agent: "kimi", State: "ok", Summary: "正常 (监听限流)"},
			{Agent: "opencode", State: "ok", Summary: "正常 (监听限流)"},
		},
	}

	// With active runs for codex + kimi, only codex & kimi appear in the quota panel.
	panel := ansi.Strip(strings.Join(m.renderQuotaPanel(30), "\n"))
	if !strings.Contains(panel, "codex") || !strings.Contains(panel, "kimi") {
		t.Fatalf("expected opened agents codex and kimi in quota panel, got:\n%s", panel)
	}
	if strings.Contains(panel, "claude") || strings.Contains(panel, "opencode") {
		t.Fatalf("expected unopened agents to be hidden when runs exist, got:\n%s", panel)
	}

	// When no runs exist, fall back to installed/available agents (claude, codex, kimi; NOT uninstalled opencode).
	m.snap.Runs = nil
	panelEmpty := ansi.Strip(strings.Join(m.renderQuotaPanel(30), "\n"))
	if !strings.Contains(panelEmpty, "claude") || !strings.Contains(panelEmpty, "codex") || !strings.Contains(panelEmpty, "kimi") {
		t.Fatalf("expected installed agents in fallback quota panel, got:\n%s", panelEmpty)
	}
	if strings.Contains(panelEmpty, "opencode") {
		t.Fatalf("expected uninstalled opencode to be excluded in fallback quota panel, got:\n%s", panelEmpty)
	}
}

func TestControlCenterHelpAndCleanTermKeymap(t *testing.T) {
	m := NewModel(nil, nil)
	m.width = 120
	m.height = 36
	m.snap = protocol.StateSnapshot{
		Projects: []protocol.Project{{ID: "p1", Name: "demo", RepoPath: "/tmp/demo"}},
		Tasks:    []protocol.Task{{ID: "t1", ProjectID: "p1", Title: "task"}},
		Runs: []protocol.Run{
			{ID: "r1", TaskID: "t1", ProjectID: "p1", Agent: "codex"},
			{ID: "r2", TaskID: "t1", ProjectID: "p1", Agent: "agy"},
		},
		Agents: []protocol.AgentInfo{
			{Name: "codex", Label: "Codex CLI", Available: true},
			{Name: "agy", Label: "Antigravity", Available: true},
		},
	}
	m.rebuildTree()
	m.selectByRunID("r1")

	// 1. In NAV mode, number keys 1..9 switch directly to the Nth run in the current task.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	m2 := next.(Model)
	if m2.currentRunID() != "r2" {
		t.Fatalf("expected key '2' in NAV mode to select 2nd run r2, got %q", m2.currentRunID())
	}

	// 2. Pressing ',' opens the unified Control Center; pressing '5' jumps to Quota modal.
	next, _ = m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{','}})
	mCC := next.(Model)
	if mCC.mode != modeModal || mCC.modal != modalControlCenter {
		t.Fatalf("expected ',' to open modalControlCenter, got mode=%v modal=%v", mCC.mode, mCC.modal)
	}
	if body := ansi.Strip(mCC.renderControlCenterBody()); !strings.Contains(body, "控制中心") || !strings.Contains(body, "Magpie") {
		t.Fatalf("unexpected control center body:\n%s", body)
	}
	next, _ = mCC.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'5'}})
	mQ := next.(Model)
	if mQ.modal != modalQuota {
		t.Fatalf("expected '5' in Control Center to open modalQuota, got %v", mQ.modal)
	}

	// 3. Pressing '?' opens the grouped Help & Command Palette modal.
	mNav := m2
	next, _ = mNav.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	mHelp := next.(Model)
	if mHelp.mode != modeModal || mHelp.modal != modalHelp {
		t.Fatalf("expected '?' to open modalHelp, got mode=%v modal=%v", mHelp.mode, mHelp.modal)
	}
	if body := ansi.Strip(mHelp.renderHelpModalBody()); !strings.Contains(body, "快捷键速查") || !strings.Contains(body, "Ctrl+]") {
		t.Fatalf("unexpected help modal body:\n%s", body)
	}

	// 4. In TERM mode, Alt+T MUST NOT toggle shellMode (keeps macOS Option/Agent shortcuts conflict-free).
	mTerm := m2
	mTerm.mode = modeTerm
	next, _ = mTerm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}, Alt: true})
	if next.(Model).shellMode {
		t.Fatalf("expected Alt+T in TERM mode NOT to be hijacked for shellMode")
	}

	// 5. Custom escape_key from config.toml works cleanly to exit TERM mode.
	mTerm.snap.EscapeKey = "ctrl+g"
	next, _ = mTerm.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	if next.(Model).mode != modeNav {
		t.Fatalf("expected custom escape_key ctrl+g to exit TERM mode to NAV, got %v", next.(Model).mode)
	}

	// 6. Buffered Double-Esc exits TERM -> NAV without forwarding Esc to the Agent,
	// while single Esc schedules a flush tick and Esc + key resets the pending state.
	mEsc := m2
	mEsc.mode = modeTerm
	after1, cmd1 := mEsc.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mAfter1 := after1.(Model)
	if mAfter1.mode != modeTerm || !mAfter1.termEscPending || cmd1 == nil {
		t.Fatalf("single Esc must stay in TERM with termEscPending=true and flush tick, got mode=%v pending=%v cmd=%v", mAfter1.mode, mAfter1.termEscPending, cmd1 != nil)
	}
	// Pressing another key flushes & clears pending Esc so Esc -> 'a' -> Esc does not exit.
	afterOther, _ := mAfter1.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if afterOther.(Model).termEscPending {
		t.Fatalf("pressing another key must clear termEscPending")
	}
	afterEscAgain, _ := afterOther.(Model).Update(tea.KeyMsg{Type: tea.KeyEsc})
	if afterEscAgain.(Model).mode != modeTerm || !afterEscAgain.(Model).termEscPending {
		t.Fatalf("Esc -> 'a' -> Esc must not trigger double-Esc exit")
	}
	// Two consecutive Esc presses exit cleanly to NAV mode and clear pending Esc.
	afterDoubleEsc, _ := afterEscAgain.(Model).Update(tea.KeyMsg{Type: tea.KeyEsc})
	mDoneEsc := afterDoubleEsc.(Model)
	if mDoneEsc.mode != modeNav || mDoneEsc.termEscPending {
		t.Fatalf("consecutive double-Esc must exit TERM mode to NAV with pending=false, got mode=%v pending=%v", mDoneEsc.mode, mDoneEsc.termEscPending)
	}

	// 7. Ctrl+\ in TERM mode directly triggers workspace Shell toggle without returning to NAV.
	mShellToggle := m2
	mShellToggle.mode = modeTerm
	afterBackslash, shellCmd := mShellToggle.Update(tea.KeyMsg{Type: tea.KeyCtrlBackslash})
	if afterBackslash.(Model).mode != modeTerm || shellCmd == nil {
		t.Fatalf("expected Ctrl+\\ in TERM mode to launch/toggle workspace shell directly, got mode=%v cmd=%v", afterBackslash.(Model).mode, shellCmd != nil)
	}
}
