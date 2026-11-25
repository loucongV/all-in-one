package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/loucongV/all-in-one/internal/protocol"
	"github.com/loucongV/all-in-one/internal/version"
)

// controlCenterItem describes an entry inside the unified `,` Control Center modal.
type controlCenterItem struct {
	id     string
	key    string
	title  string
	desc   string
	status string
	accent bool
}

func (m Model) controlCenterItems() []controlCenterItem {
	availCnt := 0
	for _, a := range m.snap.Agents {
		if a.Available {
			availCnt++
		}
	}
	toolsStatus := fmt.Sprintf("%d/%d 就绪", availCnt, len(m.snap.Agents))
	toolsAccent := false
	if inst, ok := m.backgroundRunningInstall(); ok {
		toolsStatus = "● 正在安装 " + inst.Name
		toolsAccent = true
	}

	magpieStatus := "未安装"
	magpieAccent := false
	if m.snap.Magpie.Available {
		magpieStatus = "✓ 就绪"
		magpieAccent = true
	}

	proxyStatus := "自动"
	switch m.snap.Proxy.Mode {
	case protocol.ProxyModeManual:
		proxyStatus = "手动"
	case protocol.ProxyModeDirect:
		proxyStatus = "直连"
	}
	if n := len(m.snap.Proxy.DirectAgents); n > 0 {
		proxyStatus += fmt.Sprintf(" · %d直连", n)
	}

	mobileStatus := "未开启"
	mobileAccent := false
	if m.snap.Mobile.Running {
		proto := "HTTP"
		if m.snap.Mobile.HTTPS {
			proto = "HTTPS"
		}
		if n := len(m.snap.Mobile.Sessions); n > 0 {
			mobileStatus = fmt.Sprintf("● %s · %d 会话", proto, n)
		} else {
			mobileStatus = fmt.Sprintf("● %s %s", proto, m.snap.Mobile.SelectedIP)
		}
		mobileAccent = true
	}

	quotaStatus := fmt.Sprintf("%d 个活跃", len(m.visibleQuotas()))

	mouseStatus := "默认捕获"
	mouseAccent := false
	if m.mouseDisabled {
		mouseStatus = "已开启原生右键"
		mouseAccent = true
	}

	return []controlCenterItem{
		{
			id:     "tools",
			key:    "1",
			title:  "CLI 工具管理与一键安装",
			desc:   "精准探测本机 Agent · 自定义路径 · 一键安装",
			status: toolsStatus,
			accent: toolsAccent,
		},
		{
			id:     "magpie",
			key:    "2",
			title:  "Magpie 模型路由网关",
			desc:   "打开 Magpie TUI 面板或一键安装",
			status: magpieStatus,
			accent: magpieAccent,
		},
		{
			id:     "proxy",
			key:    "3",
			title:  "网络代理与按 Agent 分流",
			desc:   "自动/手动/直连配置 · 国内 Agent 直连白名单",
			status: proxyStatus,
		},
		{
			id:     "mobile",
			key:    "4",
			title:  "手机局域网扫码同步",
			desc:   "生成局域网二维码 · 支持 HTTP/HTTPS 与手机语音",
			status: mobileStatus,
			accent: mobileAccent,
		},
		{
			id:     "quota",
			key:    "5",
			title:  "实时额度监控详情",
			desc:   "查看各 Agent 剩余额度、重置倒计时与立即刷新",
			status: quotaStatus,
		},
		{
			id:     "mouse",
			key:    "6",
			title:  "SSH 原生鼠标 / 右键粘贴模式",
			desc:   "释放鼠标给 SSH 终端直接右键粘贴（再次点击恢复）",
			status: mouseStatus,
			accent: mouseAccent,
		},
		{
			id:     "refresh",
			key:    "7",
			title:  "重新扫描工具并刷新状态",
			desc:   "同步最新本机 CLI、会话状态与额度信息",
			status: "立即执行",
		},
	}
}

func (m *Model) openControlCenterModal() {
	if m.shellMode && m.ptyDead {
		m.shellMode = false
		m.ptyDead = false
		m.attachedRunID = ""
	}
	m.modal = modalControlCenter
	m.mode = modeModal
	m.controlCursor = 0
	m.errMsg = ""
}

func (m Model) execControlCenterItem(id string) (tea.Model, tea.Cmd) {
	switch id {
	case "tools":
		m.openToolsModal()
		return m, m.fetchStateCmd()
	case "magpie":
		if m.snap.Magpie.Available {
			m.modal = modalNone
			m.mode = modeNav
			m.loading = true
			return m, func() tea.Msg {
				resp, err := m.client.EnsureMagpieTUI()
				return shellReadyMsg{resp: resp, err: err}
			}
		}
		m.openToolsModal()
		rows := m.toolRows()
		for i, r := range rows {
			if r.isMagpie {
				m.toolsCursor = i
				break
			}
		}
		return m, m.fetchStateCmd()
	case "proxy":
		m.openProxyModal()
		return m, nil
	case "mobile":
		return m, m.openMobileModal()
	case "quota":
		m.modal = modalQuota
		m.mode = modeModal
		return m, nil
	case "mouse":
		m.modal = modalNone
		m.mode = modeNav
		return m.toggleMouseMode()
	case "refresh":
		m.modal = modalNone
		m.mode = modeNav
		m.toast = "已刷新工作台状态"
		return m, m.fetchStateCmd()
	}
	return m, nil
}

func (m Model) handleControlCenterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	items := m.controlCenterItems()
	switch msg.String() {
	case ",", "q":
		m.modal = modalNone
		m.mode = modeNav
		return m, nil
	case "up", "k":
		if len(items) > 0 {
			m.controlCursor = (m.controlCursor - 1 + len(items)) % len(items)
		}
		return m, nil
	case "down", "j":
		if len(items) > 0 {
			m.controlCursor = (m.controlCursor + 1) % len(items)
		}
		return m, nil
	case "enter", " ":
		if m.controlCursor >= 0 && m.controlCursor < len(items) {
			return m.execControlCenterItem(items[m.controlCursor].id)
		}
		return m, nil
	case "1", "2", "3", "4", "5", "6", "7":
		idx := int(msg.String()[0] - '1')
		if idx >= 0 && idx < len(items) {
			return m.execControlCenterItem(items[idx].id)
		}
		return m, nil
	}
	return m, nil
}

func (m Model) renderControlCenterBody() string {
	items := m.controlCenterItems()
	const listW = 70

	var buf strings.Builder
	for i, it := range items {
		isCur := i == m.controlCursor
		if isCur {
			left := fmt.Sprintf("▎ [%s] %-22s", it.key, it.title)
			row := joinLeftRight(left, it.status, listW)
			buf.WriteString(selectedRowStyle.Width(listW).Render(row) + "\n")
			buf.WriteString("      " + softTextStyle.Render(truncate(it.desc, listW-6)) + "\n")
		} else {
			st := mutedStyle.Render(it.status)
			if it.accent {
				st = okStyle.Render(it.status)
			}
			left := fmt.Sprintf("  %s %s", keyHintStyle.Render("["+it.key+"]"), headerTitleStyle.Render(it.title))
			row := joinLeftRight(left, st, listW)
			buf.WriteString(row + "\n")
		}
	}

	title := fmt.Sprintf("◈ 控制中心 · aio %s（工具 · Magpie · 代理 · 手机 · 额度）", version.Short())
	if m.snap.DaemonVersion != "" && m.snap.DaemonVersion != version.Short() {
		title += "  " + warnStyle.Render("[daemon "+m.snap.DaemonVersion+"]")
	}
	return headerTitleStyle.Render(title) + "\n\n" +
		buf.String() + "\n" +
		mutedStyle.Render("↑/↓ 选择 · Enter 或按 1~7 直达 · Esc / , 关闭")
}

// --- Grouped Shortcut Help & Executable Command Palette (`?`) ---

type helpEntry struct {
	section string
	key     string
	desc    string
	action  string // optional executable action ID when pressing Enter
}

func (m Model) helpEntries() []helpEntry {
	esc := m.escapeKeyLabel()
	return []helpEntry{
		// Section 1: Navigation
		{section: "浏览与切换 (NAV)", key: "↑/↓ (j/k)", desc: "在项目 / 任务 / Agent 树中上下移动"},
		{section: "浏览与切换 (NAV)", key: "←/→ (h/l)", desc: "折叠 / 展开节点（在 Agent 上按 → 进入终端）"},
		{section: "浏览与切换 (NAV)", key: "Enter", desc: "进入终端（休眠自动恢复）/ 展开折叠"},
		{section: "浏览与切换 (NAV)", key: "1~9", desc: "快速切到当前任务下的第 1~9 个 Agent"},
		{section: "浏览与切换 (NAV)", key: "t", desc: "在 Agent 视图与工作区独立 Shell 终端间切换", action: "shell"},
		{section: "浏览与切换 (NAV)", key: "w", desc: "跳转到等待人工确认 (waiting_input) 的任务", action: "waiting"},
		{section: "浏览与切换 (NAV)", key: "PgUp/PgDn", desc: "向上 / 向下回看终端历史（End 回到底部）"},

		// Section 2: Tasks & Code
		{section: "任务与代码流 (NAV)", key: "p", desc: "浏览或新建本地工作目录（自动识别 Git & 多仓）", action: "add_project"},
		{section: "任务与代码流 (NAV)", key: "n", desc: "新建任务（勾选 1 个为单任务，多选自动赛跑对比）", action: "new_task"},
		{section: "任务与代码流 (NAV)", key: "+", desc: "向当前任务追加新 Agent（支持共享 Worktree 接力）", action: "add_runs"},
		{section: "任务与代码流 (NAV)", key: "r", desc: "管理、绑定或恢复该 Agent 的历史 Session", action: "resume"},
		{section: "任务与代码流 (NAV)", key: "d", desc: "横向查看当前任务的代码变更 Diff", action: "diff"},
		{section: "任务与代码流 (NAV)", key: "a", desc: "采纳（Adopt）当前胜出分支并清理其余 Worktree", action: "adopt"},
		{section: "任务与代码流 (NAV)", key: "e / F2", desc: "重命名当前任务", action: "rename"},
		{section: "任务与代码流 (NAV)", key: "s / x", desc: "停止当前会话 / 删除并清理进程树与 Session"},

		// Section 3: Terminal mode
		{section: "终端交互 (TERM)", key: "Esc×2 / " + esc, desc: "双击 Esc（防误断）或按 " + esc + " 退回导航栏（单按 Esc 仍可取消生成）"},
		{section: "终端交互 (TERM)", key: "Ctrl+\\", desc: "在对话中直接呼出 / 切回工作区独立 Shell 终端（无需先退导航）"},
		{section: "终端交互 (TERM)", key: "鼠标滚轮", desc: "直接回看历史；拖选文字自动复制，点击左侧栏切导航"},
		{section: "终端交互 (TERM)", key: "其余所有键", desc: "100% 透传给 Agent（零 macOS Option / CLI 冲突）"},

		// Section 4: System & Control Center
		{section: "系统与视图", key: ",", desc: "打开控制中心（工具安装 / Magpie / 代理 / 手机 / 额度）", action: "control"},
		{section: "系统与视图", key: "z / y / q", desc: "全屏聚焦 / 复制屏幕 / 退出界面（后台任务保持运行）"},
	}
}

func (m *Model) openHelpModal() {
	m.modal = modalHelp
	m.mode = modeModal
	m.helpCursor = 0
	m.errMsg = ""
}

func (m Model) handleHelpModalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	entries := m.helpEntries()
	switch msg.String() {
	case "?", "q":
		m.modal = modalNone
		m.mode = modeNav
		return m, nil
	case "up", "k":
		if len(entries) > 0 {
			m.helpCursor = (m.helpCursor - 1 + len(entries)) % len(entries)
		}
		return m, nil
	case "down", "j":
		if len(entries) > 0 {
			m.helpCursor = (m.helpCursor + 1) % len(entries)
		}
		return m, nil
	case "enter":
		if m.helpCursor < 0 || m.helpCursor >= len(entries) {
			return m, nil
		}
		act := entries[m.helpCursor].action
		if act == "" {
			m.modal = modalNone
			m.mode = modeNav
			return m, nil
		}
		m.modal = modalNone
		m.mode = modeNav
		switch act {
		case "control":
			m.openControlCenterModal()
			return m, nil
		case "shell":
			return m, m.toggleShellCmd()
		case "waiting":
			return m, m.jumpToNextWaiting()
		case "add_project":
			m.openAddProjectModal()
			return m, nil
		case "new_task":
			if m.currentProjectID() == "" {
				m.errMsg = "请先按 p 添加一个本地工作目录"
				return m, nil
			}
			m.openNewTaskModal(false)
			return m, nil
		case "add_runs":
			taskID := m.currentTaskID()
			if taskID == "" {
				m.errMsg = "请先选中一个已有任务再追加 Agent"
				return m, nil
			}
			m.openAddRunsModal(taskID)
			return m, nil
		case "resume":
			if runID := m.currentRunID(); runID != "" {
				return m, m.openResumeModal(runID)
			}
			m.errMsg = "请先选中一个 Agent 会话"
			return m, nil
		case "diff":
			taskID := m.currentTaskID()
			if taskID == "" {
				m.errMsg = "请先选中一个 Task 或 Run 查看 Diff 对比"
				return m, nil
			}
			m.loading = true
			return m, m.loadDiffCmd(taskID)
		case "adopt":
			runID := m.currentRunID()
			taskID := m.currentTaskID()
			if runID == "" || taskID == "" {
				m.errMsg = "请先选中一个要采纳的 Agent Run"
				return m, nil
			}
			m.openAdoptModal(taskID, runID)
			return m, nil
		case "rename":
			taskID := m.currentTaskID()
			if taskID == "" {
				m.errMsg = "请先选中一个任务再重命名"
				return m, nil
			}
			m.openRenameTaskModal(taskID)
			return m, nil
		}
	}
	return m, nil
}

func (m Model) renderHelpModalBody() string {
	entries := m.helpEntries()
	const listW = 72
	maxVis := max(10, min(18, m.height-12))

	start := 0
	if m.helpCursor >= maxVis {
		start = m.helpCursor - maxVis + 1
	}
	end := min(len(entries), start+maxVis)

	var buf strings.Builder
	lastSec := ""
	for i := start; i < end; i++ {
		e := entries[i]
		if e.section != lastSec {
			if lastSec != "" {
				buf.WriteByte('\n')
			}
			buf.WriteString(lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render("  "+e.section) + "\n")
			lastSec = e.section
		}
		isCur := i == m.helpCursor
		if isCur {
			tag := ""
			if e.action != "" {
				tag = "Enter 执行"
			}
			left := fmt.Sprintf("▎ %-11s %s", e.key, e.desc)
			row := joinLeftRight(left, tag, listW)
			buf.WriteString(selectedRowStyle.Width(listW).Render(row) + "\n")
		} else {
			keyCol := keyHintStyle.Render(fmt.Sprintf("%-11s", e.key))
			line := fmt.Sprintf("  %s %s", keyCol, softTextStyle.Render(e.desc))
			buf.WriteString(ansi.Truncate(line, listW, "…") + "\n")
		}
	}

	return headerTitleStyle.Render("? 快捷键速查与命令面板 · aio "+version.Short()) + "\n\n" +
		buf.String() + "\n" +
		mutedStyle.Render("↑/↓ 浏览 · Enter 执行高亮命令 · Esc / ? 关闭")
}
