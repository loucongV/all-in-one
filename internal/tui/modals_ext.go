package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/loucongV/all-in-one/internal/mobile"
	"github.com/loucongV/all-in-one/internal/protocol"
)

// --- Async Messages for Extended Modals ---

type proxyTestedMsg struct {
	res protocol.ProxyTestResult
	err error
}

type mobileStatusMsg struct {
	st    protocol.MobileStatus
	toast string
	err   error
}

type installStartedMsg struct {
	resp protocol.InstallAgentResponse
	err  error
}

// --- Tool Manager Item Helper ---

type toolRow struct {
	name          string
	label         string
	binary        string
	path          string
	customPath    string
	available     bool
	installCmd    string
	isMagpie      bool
	installing    bool
	hasInstallLog bool
}

func (m Model) toolRows() []toolRow {
	installMap := make(map[string]protocol.InstallSessionInfo, len(m.snap.Installs))
	for _, inst := range m.snap.Installs {
		installMap[inst.Name] = inst
	}
	rows := make([]toolRow, 0, len(m.snap.Agents)+1)
	for _, a := range m.snap.Agents {
		inst, hasLog := installMap[a.Name]
		rows = append(rows, toolRow{
			name:          a.Name,
			label:         a.Label,
			binary:        a.Binary,
			path:          a.Path,
			customPath:    a.CustomPath,
			available:     a.Available,
			installCmd:    a.InstallCmd,
			installing:    hasLog && inst.Running,
			hasInstallLog: hasLog,
		})
	}
	mg := m.snap.Magpie
	mgInst, mgHasLog := installMap["magpie"]
	rows = append(rows, toolRow{
		name:          "magpie",
		label:         "Magpie 路由网关",
		binary:        "magpie",
		path:          mg.Path,
		customPath:    mg.CustomPath,
		available:     mg.Available,
		installCmd:    mg.InstallCmd,
		isMagpie:      true,
		installing:    mgHasLog && mgInst.Running,
		hasInstallLog: mgHasLog,
	})
	return rows
}

func newStyledInput(placeholder string, limit, width int) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.CharLimit = limit
	ti.Width = width
	return ti
}

// --- Modal Openers ---

func (m *Model) openProxyModal() {
	m.modal = modalProxy
	m.mode = modeModal
	m.modalField = 0 // 0=Mode, 1=HTTP, 2=HTTPS, 3=NO_PROXY, 4=DirectAgents
	m.proxyTestMsg = ""
	m.proxyAgentCursor = 0

	cfg := m.snap.Proxy
	switch cfg.Mode {
	case protocol.ProxyModeManual:
		m.proxyModeIdx = 1
	case protocol.ProxyModeDirect:
		m.proxyModeIdx = 2
	default:
		m.proxyModeIdx = 0
	}
	m.proxyHTTPInput.SetValue(cfg.HTTPProxy)
	m.proxyHTTPSInput.SetValue(cfg.HTTPSProxy)
	m.proxyNoProxyInput.SetValue(cfg.NoProxy)
	m.proxyDirectMap = make(map[string]bool)
	for _, ag := range cfg.DirectAgents {
		m.proxyDirectMap[ag] = true
	}
	m.syncProxyFocus()
}

func (m *Model) syncProxyFocus() {
	m.proxyHTTPInput.Blur()
	m.proxyHTTPSInput.Blur()
	m.proxyNoProxyInput.Blur()
	switch m.modalField {
	case 1:
		m.proxyHTTPInput.Focus()
	case 2:
		m.proxyHTTPSInput.Focus()
	case 3:
		m.proxyNoProxyInput.Focus()
	}
}

type proxyTarget struct {
	name  string
	label string
}

func (m Model) proxyDirectTargets() []proxyTarget {
	out := make([]proxyTarget, 0, len(m.snap.Agents)+1)
	for _, a := range m.snap.Agents {
		out = append(out, proxyTarget{name: a.Name, label: a.Label})
	}
	out = append(out, proxyTarget{name: "magpie", label: "Magpie 路由网关"})
	return out
}

func (m Model) buildProxyConfigFromModal() protocol.ProxyConfig {
	modes := []protocol.ProxyMode{protocol.ProxyModeAuto, protocol.ProxyModeManual, protocol.ProxyModeDirect}
	mode := protocol.ProxyModeAuto
	if m.proxyModeIdx >= 0 && m.proxyModeIdx < len(modes) {
		mode = modes[m.proxyModeIdx]
	}
	var direct []string
	for _, t := range m.proxyDirectTargets() {
		if m.proxyDirectMap[t.name] {
			direct = append(direct, t.name)
		}
	}
	slices.Sort(direct)
	return protocol.ProxyConfig{
		Mode:         mode,
		HTTPProxy:    strings.TrimSpace(m.proxyHTTPInput.Value()),
		HTTPSProxy:   strings.TrimSpace(m.proxyHTTPSInput.Value()),
		NoProxy:      strings.TrimSpace(m.proxyNoProxyInput.Value()),
		DirectAgents: direct,
	}
}

func (m *Model) openToolsModal() {
	m.modal = modalTools
	m.mode = modeModal
	m.toolsCursor = 0
	// If any tool is currently installing in the background, focus it immediately so I -> Enter returns straight to it.
	for i, r := range m.toolRows() {
		if r.installing {
			m.toolsCursor = i
			break
		}
	}
	m.toolsEditingPath = false
	m.toolsPathInput.Blur()
	m.errMsg = ""
}

func (m *Model) openMobileModal() tea.Cmd {
	m.modal = modalMobile
	m.mode = modeModal
	m.mobileSessionCursor = 0
	m.errMsg = ""
	if m.snap.Mobile.Running {
		return m.fetchStateCmd()
	}
	m.loading = true
	return func() tea.Msg {
		st, err := m.client.MobileStart(0, false, false)
		return mobileStatusMsg{st: st, err: err}
	}
}

// --- Extended Modal Key Handlers ---

func (m Model) handleProxyModalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	targets := m.proxyDirectTargets()
	switch msg.String() {
	case "tab", "down":
		if m.modalField == 4 && msg.String() == "down" && m.proxyAgentCursor < len(targets)-1 {
			m.proxyAgentCursor++
			return m, nil
		}
		m.modalField = (m.modalField + 1) % 5
		m.syncProxyFocus()
		return m, nil
	case "shift+tab", "up":
		if m.modalField == 4 && msg.String() == "up" && m.proxyAgentCursor > 0 {
			m.proxyAgentCursor--
			return m, nil
		}
		m.modalField = (m.modalField + 4) % 5
		m.syncProxyFocus()
		return m, nil
	case "left", "right":
		if m.modalField == 0 {
			if msg.String() == "left" {
				m.proxyModeIdx = (m.proxyModeIdx + 2) % 3
			} else {
				m.proxyModeIdx = (m.proxyModeIdx + 1) % 3
			}
			return m, nil
		}
	case " ":
		if m.modalField == 0 {
			m.proxyModeIdx = (m.proxyModeIdx + 1) % 3
			return m, nil
		}
		if m.modalField == 4 && len(targets) > 0 && m.proxyAgentCursor < len(targets) {
			name := targets[m.proxyAgentCursor].name
			m.proxyDirectMap[name] = !m.proxyDirectMap[name]
			return m, nil
		}
	case "t":
		if m.modalField == 0 || m.modalField == 4 {
			cfg := m.buildProxyConfigFromModal()
			m.proxyTestMsg = "⏳ 正在探测代理连通性..."
			return m, func() tea.Msg {
				res, err := m.client.TestProxyConfig(cfg)
				return proxyTestedMsg{res: res, err: err}
			}
		}
	case "enter":
		if m.modalField >= 1 && m.modalField <= 3 {
			m.modalField++
			m.syncProxyFocus()
			return m, nil
		}
		cfg := m.buildProxyConfigFromModal()
		m.loading = true
		return m, func() tea.Msg {
			_, err := m.client.UpdateProxyConfig(cfg)
			return actionResultMsg{
				toast:       "代理配置已保存（新启动/重启的 Agent 立即生效）",
				err:         err,
				closeModal:  true,
				reloadState: true,
			}
		}
	}

	var cmd tea.Cmd
	switch m.modalField {
	case 1:
		m.proxyHTTPInput, cmd = m.proxyHTTPInput.Update(msg)
	case 2:
		m.proxyHTTPSInput, cmd = m.proxyHTTPSInput.Update(msg)
	case 3:
		m.proxyNoProxyInput, cmd = m.proxyNoProxyInput.Update(msg)
	}
	return m, cmd
}

func (m Model) handleToolsModalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.toolRows()
	if m.toolsEditingPath {
		switch msg.String() {
		case "esc":
			m.toolsEditingPath = false
			m.toolsPathInput.Blur()
			return m, nil
		case "enter":
			if m.toolsCursor < 0 || m.toolsCursor >= len(rows) {
				return m, nil
			}
			target := rows[m.toolsCursor]
			newPath := strings.TrimSpace(m.toolsPathInput.Value())
			m.toolsEditingPath = false
			m.toolsPathInput.Blur()
			m.loading = true
			return m, func() tea.Msg {
				err := m.client.SetAgentBinary(target.name, newPath)
				toast := fmt.Sprintf("已更新 %s 自定义路径", target.label)
				if newPath == "" {
					toast = fmt.Sprintf("已重置 %s 为自动探测", target.label)
				}
				return actionResultMsg{
					toast:       toast,
					err:         err,
					reloadState: true,
				}
			}
		}
		var cmd tea.Cmd
		m.toolsPathInput, cmd = m.toolsPathInput.Update(msg)
		return m, cmd
	}

	switch msg.String() {
	case "up", "k":
		if m.toolsCursor > 0 {
			m.toolsCursor--
		}
		return m, nil
	case "down", "j":
		if m.toolsCursor < len(rows)-1 {
			m.toolsCursor++
		}
		return m, nil
	case "r":
		m.toast = "已重新扫描本机 CLI 工具"
		return m, m.fetchStateCmd()
	case "b":
		if m.toolsCursor >= 0 && m.toolsCursor < len(rows) {
			cur := rows[m.toolsCursor]
			m.toolsEditingPath = true
			if cur.customPath != "" {
				m.toolsPathInput.SetValue(cur.customPath)
			} else {
				m.toolsPathInput.SetValue(cur.path)
			}
			m.toolsPathInput.Focus()
			m.toolsPathInput.CursorEnd()
		}
		return m, nil
	case "m":
		if !m.snap.Magpie.Available {
			m.errMsg = "未检测到 magpie，请先按 Enter 安装或按 b 自选路径"
			return m, nil
		}
		m.modal = modalNone
		m.loading = true
		return m, func() tea.Msg {
			resp, err := m.client.EnsureMagpieTUI()
			return shellReadyMsg{resp: resp, err: err}
		}
	case "l":
		if m.toolsCursor < 0 || m.toolsCursor >= len(rows) {
			return m, nil
		}
		cur := rows[m.toolsCursor]
		if !cur.hasInstallLog {
			m.toast = fmt.Sprintf("%s 本次运行尚无安装日志", cur.label)
			return m, nil
		}
		m.modal = modalNone
		m.loading = true
		return m, func() tea.Msg {
			resp, err := m.client.AttachInstallAgent(cur.name)
			return installStartedMsg{resp: resp, err: err}
		}
	case "enter":
		if m.toolsCursor < 0 || m.toolsCursor >= len(rows) {
			return m, nil
		}
		cur := rows[m.toolsCursor]
		if cur.installing {
			m.modal = modalNone
			m.loading = true
			return m, func() tea.Msg {
				resp, err := m.client.AttachInstallAgent(cur.name)
				return installStartedMsg{resp: resp, err: err}
			}
		}
		if cur.isMagpie && cur.available {
			m.modal = modalNone
			m.loading = true
			return m, func() tea.Msg {
				resp, err := m.client.EnsureMagpieTUI()
				return shellReadyMsg{resp: resp, err: err}
			}
		}
		if cur.available {
			m.toast = fmt.Sprintf("%s 已就绪: %s（按 b 可修改自定义路径）", cur.label, cur.path)
			return m, nil
		}
		if cur.installCmd == "" {
			m.errMsg = fmt.Sprintf("%s 暂不支持自动安装，请按 b 手动指定二进制路径", cur.label)
			return m, nil
		}
		m.modal = modalNone
		m.loading = true
		return m, func() tea.Msg {
			resp, err := m.client.InstallAgent(cur.name)
			return installStartedMsg{resp: resp, err: err}
		}
	}
	return m, nil
}

func (m Model) handleMobileModalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := m.snap.Mobile
	sessions := st.Sessions
	if m.mobileSessionCursor >= len(sessions) && len(sessions) > 0 {
		m.mobileSessionCursor = len(sessions) - 1
	}
	if m.mobileSessionCursor < 0 {
		m.mobileSessionCursor = 0
	}
	switch msg.String() {
	case "up", "k":
		if len(sessions) > 0 {
			m.mobileSessionCursor = (m.mobileSessionCursor - 1 + len(sessions)) % len(sessions)
		}
		return m, nil
	case "down", "j":
		if len(sessions) > 0 {
			m.mobileSessionCursor = (m.mobileSessionCursor + 1) % len(sessions)
		}
		return m, nil
	case "x", "delete", "backspace":
		if st.Running && len(sessions) > 0 && m.mobileSessionCursor < len(sessions) {
			target := sessions[m.mobileSessionCursor]
			m.loading = true
			return m, func() tea.Msg {
				updated, err := m.client.MobileRevokeSession(target.ID, false)
				return mobileStatusMsg{
					st:    updated,
					toast: fmt.Sprintf("已踢出手机会话: %s (%s)", target.Device, target.RemoteIP),
					err:   err,
				}
			}
		}
		return m, nil
	case "X":
		if st.Running && len(sessions) > 0 {
			m.loading = true
			return m, func() tea.Msg {
				updated, err := m.client.MobileRevokeSession("", true)
				return mobileStatusMsg{
					st:    updated,
					toast: "已注销全部已登录手机会话",
					err:   err,
				}
			}
		}
		return m, nil
	case "left", "right":
		if st.Running && len(st.LANIPs) > 1 {
			idx := 0
			for i, ip := range st.LANIPs {
				if ip == st.SelectedIP {
					idx = i
					break
				}
			}
			if msg.String() == "left" {
				idx = (idx - 1 + len(st.LANIPs)) % len(st.LANIPs)
			} else {
				idx = (idx + 1) % len(st.LANIPs)
			}
			nextIP := st.LANIPs[idx]
			return m, func() tea.Msg {
				updated, err := m.client.MobileSelectIP(nextIP)
				return mobileStatusMsg{st: updated, toast: "已切换局域网 IP: " + nextIP, err: err}
			}
		}
		return m, nil
	case "h":
		m.loading = true
		nextHTTPS := !st.HTTPS
		modeLabel := "HTTP"
		if nextHTTPS {
			modeLabel = "HTTPS"
		}
		return m, func() tea.Msg {
			updated, err := m.client.MobileStart(0, false, nextHTTPS)
			return mobileStatusMsg{st: updated, toast: "已切换手机同步协议为 " + modeLabel, err: err}
		}
	case "r":
		m.loading = true
		return m, func() tea.Msg {
			updated, err := m.client.MobileStart(0, true, st.HTTPS)
			return mobileStatusMsg{st: updated, toast: "已刷新二维码 Token 并重置全部手机会话", err: err}
		}
	case "s":
		m.loading = true
		return m, func() tea.Msg {
			updated, err := m.client.MobileStop()
			return mobileStatusMsg{st: updated, toast: "已停止手机局域网同步服务", err: err}
		}
	case "c", "y":
		if st.URL != "" {
			_ = copyToClipboard(st.URL)
			m.toast = "已复制手机连接链接"
		}
		return m, nil
	case "enter":
		if !st.Running {
			m.loading = true
			return m, func() tea.Msg {
				updated, err := m.client.MobileStart(0, false, false)
				return mobileStatusMsg{st: updated, err: err}
			}
		}
		return m, nil
	}
	return m, nil
}

// --- Extended Modal Renderers ---

func (m Model) renderProxyModalBody() string {
	modeLabels := []string{"跟随系统环境 (auto)", "手动指定 (manual)", "强制直连 (direct)"}
	var modeBuf strings.Builder
	for i, lbl := range modeLabels {
		if i == m.proxyModeIdx {
			modeBuf.WriteString(badgeNavStyle.Render(lbl) + " ")
		} else {
			modeBuf.WriteString(mutedStyle.Render(" "+lbl+" ") + " ")
		}
	}

	focusMark := func(f int, s string) string {
		if m.modalField == f {
			return lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render("▸ " + s)
		}
		return "  " + s
	}

	var directBuf strings.Builder
	for i, t := range m.proxyDirectTargets() {
		isCur := m.modalField == 4 && i == m.proxyAgentCursor
		cur := "  "
		if isCur {
			cur = "▶ "
		}
		chk := "[ ]"
		tag := mutedStyle.Render("(走代理)")
		if m.proxyDirectMap[t.name] {
			chk = "[x]"
			tag = okStyle.Render("(直连不走代理)")
		}
		row := fmt.Sprintf("%s%s %-15s %s", cur, chk, t.label, tag)
		if isCur {
			row = selectedRowStyle.Width(54).Render(fmt.Sprintf("%s%s %-15s", cur, chk, t.label))
		}
		directBuf.WriteString(row + "\n")
	}

	testLine := ""
	if m.proxyTestMsg != "" {
		testLine = "\n" + m.proxyTestMsg + "\n"
	}

	return headerTitleStyle.Render("网络代理配置（支持按 Agent 直连分流）") + "\n\n" +
		focusMark(0, "代理模式（←/→ 切换）：") + "\n  " + modeBuf.String() + "\n\n" +
		focusMark(1, "HTTP_PROXY（manual 模式生效，如 http://127.0.0.1:7897）：") + "\n  " + m.proxyHTTPInput.View() + "\n\n" +
		focusMark(2, "HTTPS_PROXY（留空自动复用 HTTP_PROXY）：") + "\n  " + m.proxyHTTPSInput.View() + "\n\n" +
		focusMark(3, "NO_PROXY（逗号分隔，如 localhost,127.0.0.1）：") + "\n  " + m.proxyNoProxyInput.View() + "\n\n" +
		focusMark(4, "直连白名单（空格勾选国内 Agent，如 Kimi 直连不走代理）：") + "\n" + directBuf.String() +
		testLine + "\n" +
		mutedStyle.Render("Tab 切换字段  ·  t 测试连通性  ·  Enter 保存  ·  Esc 取消")
}

func (m Model) renderToolsModalBody() string {
	rows := m.toolRows()
	const listW = 74
	var buf strings.Builder
	for i, r := range rows {
		isCur := i == m.toolsCursor && !m.toolsEditingPath
		status := errorStyle.Render("✗ 未安装")
		stPlain := "✗ 未安装"
		detail := r.installCmd
		if detail == "" {
			detail = "按 b 手动指定路径"
		}
		switch {
		case r.installing:
			status = warnStyle.Render("● 安装中")
			stPlain = "● 安装中"
			detail = "按 Enter 回到安装界面"
		case r.available:
			status = okStyle.Render("✓ 已就绪")
			stPlain = "✓ 已就绪"
			detail = r.path
			if r.customPath != "" {
				detail = "[自选] " + detail
			}
		case r.hasInstallLog:
			detail = "Enter 重试 · l 查看上次日志"
		}
		if isCur {
			left := fmt.Sprintf("▎ %-15s %s  %s", r.label, stPlain, truncate(detail, 38))
			buf.WriteString(selectedRowStyle.Width(listW).Render(left) + "\n")
		} else {
			buf.WriteString(fmt.Sprintf("  %-15s %s  %s\n",
				agentNameStyled(r.name),
				status,
				mutedStyle.Render(truncate(detail, 42)),
			))
		}
	}

	editBox := ""
	if m.toolsEditingPath && m.toolsCursor >= 0 && m.toolsCursor < len(rows) {
		cur := rows[m.toolsCursor]
		editBox = "\n" + headerTitleStyle.Render("指定 "+cur.label+" 二进制路径（留空重置为自动探测）：") + "\n  " +
			m.toolsPathInput.View() + "\n" +
			mutedStyle.Render("支持直接粘贴路径或 ~/.local/bin/...  ·  Enter 确认  ·  Esc 取消") + "\n"
	}

	return headerTitleStyle.Render("Agent CLI 工具管理（精准探测 · 自选路径 · 一键安装 · Magpie）") + "\n\n" +
		buf.String() + editBox + "\n" +
		mutedStyle.Render("↑/↓ 选择  ·  Enter 安装/回到安装界面  ·  l 查看安装日志  ·  b 自选路径  ·  m Magpie  ·  r 重扫  ·  Esc 关闭")
}

func (m Model) renderMobileSessionsPanel(panelW int) string {
	sessions := m.snap.Mobile.Sessions
	onlineCnt := 0
	for _, s := range sessions {
		if s.ActiveConns > 0 {
			onlineCnt++
		}
	}

	headRight := mutedStyle.Render("暂无设备")
	if len(sessions) > 0 {
		headRight = okStyle.Render(fmt.Sprintf("%d 在线", onlineCnt)) + mutedStyle.Render(fmt.Sprintf(" / %d 已登录", len(sessions)))
	}
	var buf strings.Builder
	buf.WriteString(joinLeftRight(headerTitleStyle.Render("已登录手机 Session"), headRight, panelW) + "\n")
	buf.WriteString(dividerStyle.Render(strings.Repeat("─", panelW)) + "\n")

	if len(sessions) == 0 {
		buf.WriteString(mutedStyle.Render("  手机扫码登录后将自动显示在此处，") + "\n")
		buf.WriteString(mutedStyle.Render("  支持随时查看设备/IP 并踢出下线。") + "\n")
		return buf.String()
	}

	cur := m.mobileSessionCursor
	if cur < 0 {
		cur = 0
	}
	if cur >= len(sessions) {
		cur = len(sessions) - 1
	}

	maxVis := 6
	if m.width < 112 {
		maxVis = 4
	}
	start := 0
	if cur >= maxVis {
		start = cur - maxVis + 1
	}
	end := min(len(sessions), start+maxVis)
	if start > 0 {
		buf.WriteString(mutedStyle.Render(fmt.Sprintf("  ↑ 还有 %d 个会话...", start)) + "\n")
	}

	for i := start; i < end; i++ {
		s := sessions[i]
		isCur := i == cur
		relT := formatSessionTime(s.LastSeenAt)
		if relT == "" {
			relT = "刚刚"
		}
		statePlain := "○ 空闲"
		stateStyled := mutedStyle.Render("○ 空闲")
		if s.ActiveConns > 0 {
			statePlain = "● 在线"
			stateStyled = okStyle.Render("● 在线")
		}
		rightPlain := fmt.Sprintf("%s  %s", statePlain, relT)
		labelPlain := fmt.Sprintf("%s · %s", s.Device, s.RemoteIP)
		maxLabelW := max(12, panelW-len([]rune(rightPlain))-6)

		if isCur {
			left := fmt.Sprintf("▎ %s", truncate(labelPlain, maxLabelW))
			row := joinLeftRight(left, rightPlain, panelW)
			buf.WriteString(selectedRowStyle.Width(panelW).Render(row) + "\n")
		} else {
			left := fmt.Sprintf("  %s %s", softTextStyle.Render(truncate(s.Device, max(10, maxLabelW-len(s.RemoteIP)-3))), mutedStyle.Render(s.RemoteIP))
			right := stateStyled + "  " + mutedStyle.Render(relT)
			buf.WriteString(joinLeftRight(left, right, panelW) + "\n")
		}
	}
	if end < len(sessions) {
		buf.WriteString(mutedStyle.Render(fmt.Sprintf("  ↓ 还有 %d 个会话...", len(sessions)-end)) + "\n")
	}
	return buf.String()
}

func (m Model) renderMobileModalBody() string {
	st := m.snap.Mobile
	if !st.Running {
		return headerTitleStyle.Render("手机扫码同步（局域网）") + "\n\n" +
			mutedStyle.Render("当前局域网同步服务未启动。") + "\n\n" +
			mutedStyle.Render("Enter 启动服务并生成二维码  ·  Esc 关闭")
	}

	qrStr, err := mobile.RenderTerminalQR(st.URL)
	if err != nil {
		qrStr = errorStyle.Render("生成二维码失败: " + err.Error())
	}

	protoBadge := mutedStyle.Render("HTTP (扫码免信任)")
	hintLine := "提示：按 h 切换 HTTPS 可解锁手机浏览器实时语音按钮（首次需点「继续访问」信任自签证书）"
	if st.HTTPS {
		protoBadge = okStyle.Render("HTTPS (已解锁手机内置实时语音)")
		hintLine = "提示：已启用 HTTPS（手机首次打开请点「继续访问」信任自签证书；按 h 可切回 HTTP）"
	}
	ipLine := fmt.Sprintf("局域网地址：%s   协议：%s", keyHintStyle.Render(st.SelectedIP), protoBadge)
	if len(st.LANIPs) > 1 {
		ipLine += mutedStyle.Render(fmt.Sprintf("  (共 %d 个网卡，←/→ 切换)", len(st.LANIPs)))
	}

	footerHint := "h 切换HTTP/HTTPS · ←/→ 切换IP · c 复制 · r 刷新Token · s 停止 · Esc 返回"
	if len(st.Sessions) > 0 {
		footerHint = "↑/↓ 选会话 · x 踢出 · X 全清 · h HTTP/HTTPS · ←/→ 换IP · c 复制 · r 刷Token · s 停 · Esc"
	}

	// On wide terminals, place QR on the left and Connection Info + Session Manager side-by-side
	// so the modal stays compact and never clips vertically.
	if m.width >= 112 {
		rightW := min(54, max(44, m.width-66))
		infoBlock := strings.Join([]string{
			headerTitleStyle.Render("连接信息"),
			dividerStyle.Render(strings.Repeat("─", rightW)),
			fmt.Sprintf("地址：%s %s", keyHintStyle.Render(st.SelectedIP), mutedStyle.Render(fmt.Sprintf("(共 %d 网卡，←/→)", len(st.LANIPs)))),
			fmt.Sprintf("协议：%s", protoBadge),
			mutedStyle.Render("链接：") + softTextStyle.Render(truncate(st.URL, max(20, rightW-6))),
			"",
			m.renderMobileSessionsPanel(rightW),
		}, "\n")
		cols := lipgloss.JoinHorizontal(lipgloss.Top, qrStr, "   ", lipgloss.NewStyle().Width(rightW).Render(infoBlock))
		return headerTitleStyle.Render("手机扫码同步（同一局域网实时联动 · 支持语音 · Session 管理）") + "\n\n" +
			cols + "\n\n" +
			mutedStyle.Render(hintLine) + "\n" +
			mutedStyle.Render(footerHint)
	}

	sessW := min(68, max(46, m.width-14))
	return headerTitleStyle.Render("手机扫码同步（同一局域网实时联动 · 支持语音）") + "\n\n" +
		qrStr + "\n\n" +
		ipLine + "\n" +
		mutedStyle.Render("访问链接：") + softTextStyle.Render(st.URL) + "\n\n" +
		m.renderMobileSessionsPanel(sessW) + "\n" +
		mutedStyle.Render(hintLine) + "\n" +
		mutedStyle.Render(footerHint)
}
