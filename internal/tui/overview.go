package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/loucongV/all-in-one/internal/protocol"
	"github.com/loucongV/all-in-one/internal/version"
)

var (
	overviewCardStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorDivider).
				Padding(0, 1)

	overviewAccentCardStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorPrimary).
				Padding(0, 1)

	overviewStatBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorDivider).
				Padding(0, 1)

	overviewKeycapStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#0F172A")).
				Background(colorPrimary).
				Bold(true).
				Width(7).
				Align(lipgloss.Center)

	// Avoid dark-blue slate backgrounds (#1E293B) that collapse into the same ANSI blue as
	// colorPrimary on 16-color/SSH terminals (e.g. MobaXterm); keep high contrast across
	// TrueColor, ANSI256, and 16-color profiles.
	overviewSubKeyStyle = lipgloss.NewStyle().
				Foreground(colorPrimary).
				Bold(true).
				Width(7).
				Align(lipgloss.Center)
)

// renderOverviewPane renders the brand welcome dashboard shown when no individual Agent Run
// is selected (startup, project node selected, or task node selected).
func (m Model) renderOverviewPane(w, h int) string {
	innerW := max(20, w-4)
	innerH := max(6, h-2)

	var sections []string

	// 1. Brand Hero Banner (Geometric Prism Emblem + Gradient AIO Wordmark + Live Status Pills)
	sections = append(sections, m.renderOverviewHero(innerW))

	// 2. Live Telemetry Metric Strip (Projects / Tasks / Running / Waiting / Mobile)
	sections = append(sections, m.renderOverviewStats(innerW))

	// 3. Context Banner (when a Project or Task node is selected on the sidebar)
	if ctxCard := m.renderOverviewContext(innerW); ctxCard != "" {
		sections = append(sections, ctxCard)
	}

	// 4. Main Content Grid: Two equal-height side-by-side cards on wide terminals, stacked on narrow screens.
	if innerW >= 82 {
		gap := 2
		leftW := (innerW - gap) / 2
		rightW := innerW - gap - leftW
		leftLines := m.overviewAgentsCardLines(max(16, leftW-4))
		rightLines := m.overviewGuideCardLines(max(16, rightW-4))
		maxH := max(len(leftLines), len(rightLines))
		leftCard := overviewCardStyle.Width(leftW - 2).Height(maxH).MaxWidth(leftW).Render(strings.Join(leftLines, "\n"))
		rightCard := overviewCardStyle.Width(rightW - 2).Height(maxH).MaxWidth(rightW).Render(strings.Join(rightLines, "\n"))
		grid := lipgloss.JoinHorizontal(lipgloss.Top, leftCard, strings.Repeat(" ", gap), rightCard)
		sections = append(sections, grid)
	} else {
		leftLines := m.overviewAgentsCardLines(max(16, innerW-4))
		rightLines := m.overviewGuideCardLines(max(16, innerW-4))
		sections = append(sections, overviewCardStyle.Width(innerW-2).MaxWidth(innerW).Render(strings.Join(leftLines, "\n")))
		sections = append(sections, overviewCardStyle.Width(innerW-2).MaxWidth(innerW).Render(strings.Join(rightLines, "\n")))
	}

	body := strings.Join(sections, "\n")
	lines := clampScreenLines(body, innerW, innerH)
	content := strings.Join(lines, "\n")

	boxStyle := mainPaneStyle
	if m.mode == modeNav {
		boxStyle = mainPaneNavStyle
	}
	return boxStyle.Width(w - 2).Height(h - 2).MaxWidth(w).MaxHeight(h).Render(content)
}

// renderOverviewHero draws the signature AIO orbital-prism emblem and gradient wordmark.
func (m Model) renderOverviewHero(w int) string {
	// Gradient colors from Sky -> Cyan -> Emerald -> Violet
	c1 := lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8")).Bold(true)
	c2 := lipgloss.NewStyle().Foreground(lipgloss.Color("#22D3EE")).Bold(true)
	c3 := lipgloss.NewStyle().Foreground(lipgloss.Color("#34D399")).Bold(true)
	c4 := lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA")).Bold(true)
	cOrg := lipgloss.NewStyle().Foreground(colorAgentClaude).Bold(true)

	// Compact header for ultra-narrow screens.
	if w < 58 {
		title := c1.Render("◈") + " " + c1.Render("A") + c2.Render("I") + c3.Render("O") + "  " +
			headerTitleStyle.Render("多 Agent 编排工作台")
		return title + "\n" + dividerStyle.Render(strings.Repeat("─", w))
	}

	// Geometric 4-node orbital hub emblem — strictly 9 display cells wide on every row,
	// mirroring assets/icon.svg with spokes connecting all 4 Agent nodes into the core.
	spoke := lipgloss.NewStyle().Foreground(colorMuted)
	frame := c2
	emblem := []string{
		"    " + c1.Render("●") + "    ",
		"  " + frame.Render("╭─") + spoke.Render("┴") + frame.Render("─╮") + "  ",
		cOrg.Render("●") + spoke.Render("─") + frame.Render("┤") + headerTitleStyle.Render(">_ ") + frame.Render("├") + spoke.Render("─") + c3.Render("●"),
		"  " + frame.Render("╰─") + spoke.Render("┬") + frame.Render("─╯") + "  ",
		"    " + c4.Render("●") + "    ",
	}

	// Uniform 2-cell thick geometric block wordmark (strictly 19 cells wide on rows 0..2).
	wordmark := []string{
		c1.Render(" ▄███▄ ") + "  " + c2.Render("██") + "  " + c3.Render("▄████▄"),
		c1.Render("██▄▄▄██") + "  " + c2.Render("██") + "  " + c3.Render("██  ██"),
		c1.Render("██   ██") + "  " + c2.Render("██") + "  " + c3.Render("▀████▀"),
		softTextStyle.Render("All-in-One "+version.Short()) + mutedStyle.Render("  ·  Multi-Agent CLI Orchestrator"),
		m.renderOverviewHeroPills(),
	}

	var rows []string
	for i := range emblem {
		line := " " + emblem[i] + "   " + wordmark[i]
		if ansi.StringWidth(line) > w {
			line = ansi.Truncate(line, w, "…")
		}
		rows = append(rows, line)
	}
	rows = append(rows, dividerStyle.Render(strings.Repeat("─", w)))
	return strings.Join(rows, "\n")
}

func (m Model) renderOverviewHeroPills() string {
	var pills []string

	// Proxy badge
	switch m.snap.Proxy.Mode {
	case protocol.ProxyModeManual:
		pills = append(pills, lipgloss.NewStyle().Foreground(colorPrimary).Render("⇄ 代理:手动"))
	case protocol.ProxyModeDirect:
		pills = append(pills, mutedStyle.Render("⇄ 代理:直连"))
	default:
		pills = append(pills, lipgloss.NewStyle().Foreground(colorSoftText).Render("⇄ 代理:自动"))
	}

	// Magpie badge
	if m.snap.Magpie.Available {
		pills = append(pills, okStyle.Render("◈ Magpie 就绪"))
	}

	// Mobile LAN sync badge
	if m.snap.Mobile.Running {
		pills = append(pills, okStyle.Render("● 手机同步:"+m.snap.Mobile.SelectedIP))
	} else {
		pills = append(pills, mutedStyle.Render("○ 手机同步:, → 4"))
	}

	return strings.Join(pills, mutedStyle.Render("  ·  "))
}

func (m Model) renderOverviewStats(w int) string {
	running := 0
	waiting := 0
	done := 0
	for _, r := range m.snap.Runs {
		switch r.Status {
		case protocol.StatusRunning, protocol.StatusStarting:
			running++
		case protocol.StatusWaitingInput:
			waiting++
		case protocol.StatusDone:
			done++
		}
	}

	availAgents := 0
	for _, a := range m.snap.Agents {
		if a.Available {
			availAgents++
		}
	}

	if w < 64 {
		line := fmt.Sprintf("目录 %d · 任务 %d · 运行 %d · 待确认 %d · CLI %d/%d",
			len(m.snap.Projects), len(m.snap.Tasks), running, waiting, availAgents, len(m.snap.Agents))
		return softTextStyle.Render(ansi.Truncate(line, w, "…"))
	}

	type statItem struct {
		label string
		val   string
		style lipgloss.Style
	}
	waitStyle := mutedStyle
	if waiting > 0 {
		waitStyle = warnStyle
	}
	runStyle := softTextStyle
	if running > 0 {
		runStyle = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	}

	items := []statItem{
		{"工作目录", fmt.Sprintf("%d", len(m.snap.Projects)), headerTitleStyle},
		{"任务 / 会话", fmt.Sprintf("%d / %d", len(m.snap.Tasks), len(m.snap.Runs)), headerTitleStyle},
		{"运行中", fmt.Sprintf("● %d", running), runStyle},
		{"等待确认", fmt.Sprintf("▲ %d", waiting), waitStyle},
		{"就绪 CLI", fmt.Sprintf("%d/%d", availAgents, len(m.snap.Agents)), okStyle},
	}

	gap := 1
	avail := w - gap*(len(items)-1)
	baseW := max(10, avail/len(items))
	rem := max(0, avail-baseW*len(items))
	var boxes []string
	for i, it := range items {
		bw := baseW
		if i < rem {
			bw++
		}
		inner := mutedStyle.Render(it.label) + "\n" + it.style.Render(it.val)
		box := overviewStatBoxStyle.Width(bw - 2).MaxWidth(bw).Render(inner)
		boxes = append(boxes, box)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, joinWithGap(boxes, strings.Repeat(" ", gap))...)
}

func joinWithGap(parts []string, sep string) []string {
	if len(parts) <= 1 {
		return parts
	}
	out := make([]string, 0, len(parts)*2-1)
	for i, p := range parts {
		if i > 0 {
			out = append(out, sep)
		}
		out = append(out, p)
	}
	return out
}

func (m Model) renderOverviewContext(w int) string {
	if len(m.nodes) == 0 || m.sel < 0 || m.sel >= len(m.nodes) {
		return ""
	}
	n := m.nodes[m.sel]
	innerW := max(16, w-4)

	switch n.kind {
	case nodeProject:
		proj, ok := m.findProject(n.projectID)
		if !ok {
			return ""
		}
		kindTag := "普通目录 (local)"
		if proj.IsMultiRepo {
			kindTag = fmt.Sprintf("多仓组合 Worktree (%d 个子仓)", proj.SubRepoCount)
		} else if proj.IsGitRepo {
			kindTag = "Git 仓库 (支持共享/独立 Worktree)"
		}
		cnt, _, _ := m.projectTaskSummary(proj.ID)
		header := joinLeftRight(
			brandIconStyle.Render("▾")+" "+headerTitleStyle.Render(proj.Name)+"  "+okStyle.Render(kindTag),
			mutedStyle.Render(fmt.Sprintf("%d 个任务", cnt)),
			innerW,
		)
		pathLine := mutedStyle.Render("路径：") + softTextStyle.Render(truncate(proj.RepoPath, max(12, innerW-8)))
		hintLine := mutedStyle.Render("快捷键：") +
			keyHintStyle.Render("n") + softTextStyle.Render(" 新建任务  ") +
			keyHintStyle.Render("t") + softTextStyle.Render(" 打开终端  ") +
			keyHintStyle.Render(",") + softTextStyle.Render(" 控制中心  ") +
			keyHintStyle.Render("x") + softTextStyle.Render(" 移除目录")
		return overviewAccentCardStyle.Width(w - 2).MaxWidth(w).Render(header + "\n" + pathLine + "\n" + hintLine)

	case nodeTask:
		task, ok := m.findTask(n.taskID)
		if !ok {
			return ""
		}
		proj, _ := m.findProject(task.ProjectID)
		modeLabel := "单任务"
		if task.Mode == "compare" {
			modeLabel = "⇄ 多 Agent 对比"
		}
		var runTags []string
		for _, r := range m.snap.Runs {
			if r.TaskID == task.ID {
				runTags = append(runTags, statusIcon(r.Status)+" "+agentNameStyled(r.Agent))
			}
		}
		header := joinLeftRight(
			brandIconStyle.Render("▸")+" "+headerTitleStyle.Render(proj.Name+" / "+task.Title)+"  "+mutedStyle.Render(modeLabel),
			mutedStyle.Render(task.WorkMode),
			innerW,
		)
		runsLine := mutedStyle.Render("会话：") + strings.Join(runTags, "   ")
		if len(runTags) == 0 {
			runsLine = mutedStyle.Render("会话：暂无，按 + 追加 Agent")
		}
		hintLine := mutedStyle.Render("操作：") +
			keyHintStyle.Render("↓/Enter") + softTextStyle.Render(" 展开会话  ") +
			keyHintStyle.Render("+") + softTextStyle.Render(" 追加Agent  ") +
			keyHintStyle.Render("d") + softTextStyle.Render(" 查看Diff  ") +
			keyHintStyle.Render("e") + softTextStyle.Render(" 改名  ") +
			keyHintStyle.Render("x") + softTextStyle.Render(" 清理任务")
		return overviewAccentCardStyle.Width(w - 2).MaxWidth(w).Render(header + "\n" + runsLine + "\n" + hintLine)
	}
	return ""
}

func (m Model) overviewAgentsCardLines(innerW int) []string {
	title := joinLeftRight(
		brandIconStyle.Render("◈")+" "+headerTitleStyle.Render("Agent 引擎矩阵"),
		mutedStyle.Render("按 , 管理 / 安装"),
		innerW,
	)

	lines := []string{title, dividerStyle.Render(strings.Repeat("─", innerW))}
	showBinCol := innerW >= 38

	for _, a := range m.snap.Agents {
		binDisp := a.Binary
		if a.CustomPath != "" {
			binDisp = "自选:" + filepath.Base(a.Path)
		}
		binCol := ""
		if showBinCol {
			binCol = "  " + mutedStyle.Render(fmt.Sprintf("%-13s", truncate(binDisp, 13)))
		}
		var left, right string
		if a.Available {
			nameCol := lipgloss.NewStyle().Foreground(agentColor(a.Name)).Bold(true).Render(fmt.Sprintf("%-12s", a.Label))
			left = okStyle.Render("●") + " " + nameCol + binCol
			right = okStyle.Render("✓ 就绪")
		} else {
			nameCol := mutedStyle.Render(fmt.Sprintf("%-12s", a.Label))
			left = mutedStyle.Render("○") + " " + nameCol + binCol
			right = mutedStyle.Render("未安装")
		}
		lines = append(lines, joinLeftRight(left, right, innerW))
	}

	// Magpie gateway row (aligns horizontally with the bottom footer row of the right card).
	lines = append(lines, dividerStyle.Render(strings.Repeat("─", innerW)))
	mgBin := ""
	if showBinCol {
		mgBin = "  " + mutedStyle.Render(fmt.Sprintf("%-13s", "magpie"))
	}
	mgLeft := mutedStyle.Render("○") + " " + mutedStyle.Render(fmt.Sprintf("%-12s", "Magpie")) + mgBin
	mgRight := mutedStyle.Render("未安装 · ,安装")
	if m.snap.Magpie.Available {
		mgLeft = okStyle.Render("●") + " " + lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(fmt.Sprintf("%-12s", "Magpie")) + mgBin
		mgRight = okStyle.Render("✓ 路由就绪")
	}
	lines = append(lines, joinLeftRight(mgLeft, mgRight, innerW))
	return lines
}

func (m Model) overviewGuideCardLines(innerW int) []string {
	title := joinLeftRight(
		brandIconStyle.Render("⌘")+" "+headerTitleStyle.Render("快速操作台"),
		mutedStyle.Render("双击Esc / "+m.escapeKeyLabel()+" 回导航"),
		innerW,
	)

	row := func(primary bool, key, desc string) string {
		var k string
		if primary {
			k = overviewKeycapStyle.Render(key)
		} else {
			k = overviewSubKeyStyle.Render("[" + key + "]")
		}
		return ansi.Truncate(k+"  "+softTextStyle.Render(desc), innerW, "…")
	}

	lines := []string{
		title,
		dividerStyle.Render(strings.Repeat("─", innerW)),
		row(true, "p", "浏览 / 新建工作目录（自动识别 Git & 多仓）"),
		row(true, "n / +", "新建任务（单/多 Agent 赛跑）· 追加共享接力"),
		row(false, "d / a", "横向查看 Diff 对比 · 一键采纳胜出分支并清理"),
		row(false, "t / r", "独立持久化工作区终端 · 历史 Session 绑定续聊"),
		row(false, ",", "控制中心（CLI 安装 · Magpie · 代理 · 手机 · 额度）"),
		row(false, "?", "分组快捷键速查与可执行命令面板"),
		dividerStyle.Render(strings.Repeat("─", innerW)),
		ansi.Truncate(
			mutedStyle.Render("导航：")+
				keyHintStyle.Render("1~9")+mutedStyle.Render(" 切Agent  ·  ")+
				keyHintStyle.Render("w")+mutedStyle.Render(" 跳待确认  ·  ")+
				keyHintStyle.Render("PgUp")+mutedStyle.Render(" 回看历史"),
			innerW, "…",
		),
	}
	return lines
}
