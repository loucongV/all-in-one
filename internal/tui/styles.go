package tui

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/loucongV/all-in-one/internal/protocol"
)

var (
	colorPrimary   = lipgloss.Color("#38BDF8") // Sky-400
	colorTermFocus = lipgloss.Color("#22C55E") // Green-500 for TERM mode
	colorWarn      = lipgloss.Color("#F59E0B") // Amber-500 for WAITING
	colorDanger    = lipgloss.Color("#EF4444") // Red-500 for ERROR
	colorMuted     = lipgloss.Color("#64748B") // Slate-500
	colorDim       = lipgloss.Color("#475569") // Slate-600 (tree guides & subtle tags)
	colorDivider   = lipgloss.Color("#334155") // Slate-700 (clean soft dividers)
	colorSubtle    = lipgloss.Color("#334155") // Unfocused border
	colorText      = lipgloss.Color("#E2E8F0") // Slate-200
	colorSoftText  = lipgloss.Color("#94A3B8") // Slate-400

	colorSelected         = lipgloss.Color("#0369A1") // Vivid sky-700 for focused selection
	colorSelectedInactive = lipgloss.Color("#273449") // Refined slate-750 when focus is in TERM

	// Per-agent signature brand colors.
	colorAgentClaude   = lipgloss.Color("#FB923C") // Warm orange
	colorAgentCodex    = lipgloss.Color("#34D399") // Emerald teal
	colorAgentAgy      = lipgloss.Color("#60A5FA") // Sky indigo
	colorAgentCursor   = lipgloss.Color("#A78BFA") // Soft violet
	colorAgentKimi     = lipgloss.Color("#22D3EE") // Crisp cyan
	colorAgentOpenCode = lipgloss.Color("#F472B6") // Coral pink

	sidebarStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorSubtle).
			Padding(0, 1)

	sidebarFocusedStyle = sidebarStyle.
				BorderForeground(colorPrimary)

	mainPaneStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorSubtle).
			Padding(0, 1)

	mainPaneTermStyle = mainPaneStyle.
				BorderForeground(colorTermFocus)

	mainPaneNavStyle = mainPaneStyle.
				BorderForeground(colorPrimary)

	mainPaneShellStyle = mainPaneStyle.
				BorderForeground(colorAgentCursor)

	headerTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorText)

	brandIconStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorPrimary)

	dividerStyle   = lipgloss.NewStyle().Foreground(colorDivider)
	treeGuideStyle = lipgloss.NewStyle().Foreground(colorDim)
	softTextStyle  = lipgloss.NewStyle().Foreground(colorSoftText)

	badgeNavStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#0F172A")).
			Background(colorPrimary).
			Padding(0, 1)

	badgeTermStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#052E16")).
			Background(colorTermFocus).
			Padding(0, 1)

	badgeWarnStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#451A03")).
			Background(colorWarn).
			Padding(0, 1)

	badgeShellStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#1E1B4B")).
			Background(colorAgentCursor).
			Padding(0, 1)

	selectedRowStyle = lipgloss.NewStyle().
				Background(colorSelected).
				Foreground(lipgloss.Color("#FFFFFF")).
				Bold(true)

	selectedInactiveRowStyle = lipgloss.NewStyle().
					Background(colorSelectedInactive).
					Foreground(lipgloss.Color("#F8FAFC")).
					Bold(true)

	keyHintStyle = lipgloss.NewStyle().
			Foreground(colorPrimary).
			Bold(true)

	mutedStyle = lipgloss.NewStyle().Foreground(colorMuted)
	errorStyle = lipgloss.NewStyle().Foreground(colorDanger).Bold(true)
	okStyle    = lipgloss.NewStyle().Foreground(colorTermFocus).Bold(true)
	warnStyle  = lipgloss.NewStyle().Foreground(colorWarn).Bold(true)

	modalBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.DoubleBorder()).
			BorderForeground(colorPrimary).
			Padding(1, 2)

	diffAddStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#4ADE80"))
	diffDelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#F87171"))
	diffHunkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#60A5FA"))
)

func agentColor(agent string) lipgloss.Color {
	switch agent {
	case "claude":
		return colorAgentClaude
	case "codex":
		return colorAgentCodex
	case "agy":
		return colorAgentAgy
	case "cursor":
		return colorAgentCursor
	case "kimi":
		return colorAgentKimi
	case "opencode":
		return colorAgentOpenCode
	default:
		return colorText
	}
}

func agentNameStyled(agent string) string {
	return lipgloss.NewStyle().Foreground(agentColor(agent)).Bold(true).Render(agent)
}

func statusSymbol(st protocol.RunStatus) string {
	switch st {
	case protocol.StatusStarting, protocol.StatusRunning:
		return "●"
	case protocol.StatusWaitingInput:
		return "▲"
	case protocol.StatusDone:
		return "✓"
	case protocol.StatusError:
		return "✗"
	default:
		return "○"
	}
}

func statusShortText(st protocol.RunStatus) string {
	switch st {
	case protocol.StatusStarting:
		return "启动"
	case protocol.StatusRunning:
		return "运行中"
	case protocol.StatusWaitingInput:
		return "待确认"
	case protocol.StatusDone:
		return "完成"
	case protocol.StatusError:
		return "出错"
	default:
		return "停止"
	}
}

func statusShortStyled(st protocol.RunStatus) string {
	switch st {
	case protocol.StatusStarting, protocol.StatusRunning:
		return lipgloss.NewStyle().Foreground(colorPrimary).Render(statusShortText(st))
	case protocol.StatusWaitingInput:
		return warnStyle.Render(statusShortText(st))
	case protocol.StatusDone:
		return okStyle.Render(statusShortText(st))
	case protocol.StatusError:
		return errorStyle.Render(statusShortText(st))
	default:
		return mutedStyle.Render(statusShortText(st))
	}
}

func statusIcon(st protocol.RunStatus) string {
	switch st {
	case protocol.StatusStarting, protocol.StatusRunning:
		return lipgloss.NewStyle().Foreground(colorPrimary).Render("●")
	case protocol.StatusWaitingInput:
		return warnStyle.Render("▲")
	case protocol.StatusDone:
		return okStyle.Render("✓")
	case protocol.StatusError:
		return errorStyle.Render("✗")
	default:
		return mutedStyle.Render("○")
	}
}

func statusLabel(st protocol.RunStatus) string {
	switch st {
	case protocol.StatusStarting:
		return lipgloss.NewStyle().Foreground(colorPrimary).Render("● 启动中")
	case protocol.StatusRunning:
		return lipgloss.NewStyle().Foreground(colorPrimary).Render("● 运行中")
	case protocol.StatusWaitingInput:
		return warnStyle.Render("▲ 待确认")
	case protocol.StatusDone:
		return okStyle.Render("✓ 已完成")
	case protocol.StatusError:
		return errorStyle.Render("✗ 出错")
	default:
		return mutedStyle.Render("○ 已停止")
	}
}
