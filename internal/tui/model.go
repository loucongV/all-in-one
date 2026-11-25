package tui

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/gorilla/websocket"
	"github.com/loucongV/all-in-one/internal/daemon"
	"github.com/loucongV/all-in-one/internal/protocol"
	"github.com/loucongV/all-in-one/internal/version"
)

type focusMode int

const (
	modeNav focusMode = iota
	modeTerm
	modeDiff
	modeModal
)

type modalKind int

const (
	modalNone modalKind = iota
	modalAddProject
	modalNewTask
	modalAddRuns
	modalRenameTask
	modalAdopt
	modalConfirmDelete
	modalResume
	modalQuota
	modalPreview
	modalProxy
	modalTools
	modalMobile
	modalControlCenter
	modalHelp
)

type nodeKind int

const (
	nodeProject nodeKind = iota
	nodeTask
	nodeRun
)

type treeNode struct {
	kind      nodeKind
	projectID string
	taskID    string
	runID     string
	label     string
}

// --- Bubble Tea Messages ---

type stateLoadedMsg struct {
	snap protocol.StateSnapshot
	err  error
}

type daemonEventMsg struct {
	ev protocol.DaemonEvent
}

type eventWSErrMsg struct {
	err error
}

type ptyConnectedMsg struct {
	runID string
	gen   uint64
	conn  *websocket.Conn
	err   error
}

type ptyScreenMsg struct {
	runID        string
	gen          uint64
	screen       string
	scrollOffset int
	scrollMax    int
	cursor       *protocol.CursorPos
	links        []protocol.LinkSpan
}

type ptyClosedMsg struct {
	runID string
	gen   uint64
}

type diffLoadedMsg struct {
	diff protocol.TaskDiffResponse
	err  error
}

type runSessionsLoadedMsg struct {
	runID    string
	sessions []protocol.CLISessionInfo
	err      error
}

type shellReadyMsg struct {
	resp protocol.EnsureShellResponse
	err  error
}

type termEscFlushMsg struct {
	seq uint64
}

type actionResultMsg struct {
	toast         string
	err           error
	selectRunID   string
	selectTaskID  string
	reattachRunID string
	enterTerm     bool
	closeModal    bool
	reloadState   bool
	switchToDiff  bool
	exitDiff      bool
}

type Model struct {
	client     *daemon.Client
	cursorSync *CursorSyncWriter
	cwd        string

	width  int
	height int

	mode           focusMode
	fullscreen     bool
	mouseDisabled  bool
	colorProf      colorprofile.Profile
	termEscPending bool
	termEscSeq     uint64

	snap              protocol.StateSnapshot
	nodes             []treeNode
	sel               int
	sidebarScroll     int
	collapsedProjects map[string]bool
	collapsedTasks    map[string]bool

	// Active PTY connection
	attachedRunID      string
	lastPTYRunID       string
	lastPTYCols        int
	lastPTYRows        int
	ptyGen             uint64
	ptyConnecting      bool
	ptyDead            bool
	ptyConn            *websocket.Conn
	ptyScreen          string
	ptyDeferredScreen  string
	ptyScrollOffset    int
	ptyScrollMax       int
	ptyCursor          *protocol.CursorPos
	ptyLinks           []protocol.LinkSpan
	ptyDeferredLinks   []protocol.LinkSpan
	pendingSelectRunID string
	pendingEnterTerm   bool

	// In-pane mouse drag text selection (auto-copies on release)
	termSel      textSel
	copyNotice   string
	copyNoticeAt time.Time

	// Embedded persistent workspace shell terminal (t / Alt+T)
	shellMode bool
	shellID   string
	shellDir  string
	shellBin  string

	// Daemon global event WS
	evConn *websocket.Conn

	// Compare diff panel state
	diffData     *protocol.TaskDiffResponse
	diffRunIdx   int
	diffViewport viewport.Model

	// Modals
	modal              modalKind
	modalIsComp        bool // true for Compare, false for Single Task
	modalField         int
	titleInput         textinput.Model
	promptInput        textinput.Model
	magpieInput        textinput.Model
	pathInput          textinput.Model
	mkdirInput         textinput.Model
	creatingDir        bool
	mkdirInitGit       bool
	picker             dirPicker
	manualPath         bool
	commitInput        textinput.Model
	agentChecked       map[string]bool
	agentCursor        int
	permOptions        []protocol.PermMode
	permIdx            int
	workModeOpts       []string
	workModeIdx        int
	workModeManual     bool
	mergeOptions       []protocol.MergeStrategy
	mergeIdx           int
	adoptTaskID        string
	adoptRunID         string
	deleteKind         nodeKind
	deleteTarget       string
	deleteLabel        string
	deleteCleanSession bool
	addRunsTaskID      string
	renameTaskID       string
	resumeRunID        string
	resumeModeIdx      int
	resumeModes        []string
	resumeSessions     []protocol.CLISessionInfo
	resumeLoading      bool
	resumeCursor       int
	resumeScroll       int
	sessionInput       textinput.Model

	// Proxy settings modal state
	proxyModeIdx      int
	proxyHTTPInput    textinput.Model
	proxyHTTPSInput   textinput.Model
	proxyNoProxyInput textinput.Model
	proxyDirectMap    map[string]bool
	proxyAgentCursor  int
	proxyTestMsg      string

	// Agent tools / custom path / installer modal state
	toolsCursor      int
	toolsEditingPath bool
	toolsPathInput   textinput.Model

	// Unified control center (`,`), help panel (`?`), and mobile session cursors
	controlCursor       int
	helpCursor          int
	mobileSessionCursor int

	// Built-in local file preview modal (clicking file:// links)
	previewData       filePreviewData
	previewViewport   viewport.Model
	previewReturnMode focusMode

	// Feedback banners
	toast       string
	errMsg      string
	staleDaemon bool
	loading     bool
}

func NewModel(client *daemon.Client, cursorSync *CursorSyncWriter) Model {
	cwd, _ := os.Getwd()
	stale := false
	if client != nil {
		if exe, err := os.Executable(); err == nil {
			wantFP := daemon.BinFingerprint(exe)
			if info, err := client.HealthInfo(); err == nil && info.BinFingerprint != wantFP {
				stale = true
			}
		}
	}

	ti := textinput.New()
	ti.Placeholder = "例如: refactor-auth"
	ti.CharLimit = 80
	ti.Width = 48

	pi := textinput.New()
	pi.Placeholder = "输入自动派发给选中 Agent 的 Prompt..."
	pi.CharLimit = 1000
	pi.Width = 56

	pathIn := textinput.New()
	pathIn.Placeholder = "/path/to/workspace"
	pathIn.SetValue(cwd)
	pathIn.CharLimit = 256
	pathIn.Width = 52

	ci := textinput.New()
	ci.Placeholder = "留空则自动生成 Commit Message"
	ci.CharLimit = 200
	ci.Width = 52

	si := textinput.New()
	si.Placeholder = "粘贴 Session ID，例如 01a029f1-..."
	si.CharLimit = 128
	si.Width = 52

	return Model{
		client:             client,
		cursorSync:         cursorSync,
		cwd:                cwd,
		mode:               modeNav,
		collapsedProjects:  make(map[string]bool),
		collapsedTasks:     make(map[string]bool),
		titleInput:         ti,
		promptInput:        pi,
		magpieInput:        newStyledInput("可选：如 deepseek/deepseek-v4-pro（留空直连）", 120, 52),
		pathInput:          pathIn,
		mkdirInput:         newStyledInput("输入新文件夹名称（支持多级如 apps/demo）", 160, 50),
		commitInput:        ci,
		agentChecked:       make(map[string]bool),
		permOptions:        []protocol.PermMode{protocol.PermDefault, protocol.PermAuto, protocol.PermPlan, protocol.PermYolo},
		workModeOpts:       []string{"shared", "local", "isolated"},
		mergeOptions:       []protocol.MergeStrategy{protocol.MergeSquash, protocol.MergeCommit, protocol.MergeRebase},
		resumeModes:        []string{"picker", "last", "fresh", "id"},
		sessionInput:       si,
		deleteCleanSession: true,
		proxyHTTPInput:     newStyledInput("http://127.0.0.1:7897", 200, 52),
		proxyHTTPSInput:    newStyledInput("留空则自动复用 HTTP_PROXY", 200, 52),
		proxyNoProxyInput:  newStyledInput("localhost,127.0.0.1", 400, 52),
		proxyDirectMap:     make(map[string]bool),
		toolsPathInput:     newStyledInput("/path/to/cli-binary（留空恢复自动探测）", 256, 58),
		diffViewport:       viewport.New(80, 20),
		colorProf:          colorprofile.TrueColor,
		staleDaemon:        stale,
	}
}

func (m *Model) SetColorProfile(p colorprofile.Profile) {
	m.colorProf = p
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.fetchStateCmd(),
		m.connectEventsCmd(),
	)
}

func (m Model) fetchStateCmd() tea.Cmd {
	return func() tea.Msg {
		snap, err := m.client.State()
		return stateLoadedMsg{snap: snap, err: err}
	}
}

func (m Model) connectEventsCmd() tea.Cmd {
	return func() tea.Msg {
		conn, err := m.client.ConnectEvents()
		if err != nil {
			return eventWSErrMsg{err: err}
		}
		var ev protocol.DaemonEvent
		if err := conn.ReadJSON(&ev); err != nil {
			_ = conn.Close()
			return eventWSErrMsg{err: err}
		}
		_ = conn.Close()
		return daemonEventMsg{ev: ev}
	}
}

func (m Model) connectPTYCmd(runID string, gen uint64, cols, rows int) tea.Cmd {
	return func() tea.Msg {
		conn, err := m.client.ConnectPTY(runID, false)
		if err != nil {
			return ptyConnectedMsg{runID: runID, gen: gen, err: err}
		}
		_ = daemon.SendPTYResize(conn, cols, rows)
		return ptyConnectedMsg{runID: runID, gen: gen, conn: conn}
	}
}

func readPTYFrameCmd(runID string, gen uint64, conn *websocket.Conn) tea.Cmd {
	return func() tea.Msg {
		if conn == nil {
			return ptyClosedMsg{runID: runID, gen: gen}
		}
		var msg protocol.PTYMessage
		if err := conn.ReadJSON(&msg); err != nil {
			return ptyClosedMsg{runID: runID, gen: gen}
		}
		if msg.Type == "closed" {
			return ptyClosedMsg{runID: runID, gen: gen}
		}
		if msg.Type == "screen" {
			return ptyScreenMsg{
				runID:        runID,
				gen:          gen,
				screen:       msg.Data,
				scrollOffset: msg.ScrollOffset,
				scrollMax:    msg.ScrollMax,
				cursor:       msg.Cursor,
				links:        msg.Links,
			}
		}
		return ptyScreenMsg{runID: runID, gen: gen}
	}
}

func (m Model) loadDiffCmd(taskID string) tea.Cmd {
	return func() tea.Msg {
		diff, err := m.client.TaskDiff(taskID)
		return diffLoadedMsg{diff: diff, err: err}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.termSel.clear()
		m.resizeViewportAndPTY()
		return m, tea.ClearScreen

	case stateLoadedMsg:
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			return m, nil
		}
		m.snap = msg.snap
		m.rebuildTree()
		if m.pendingSelectRunID != "" && m.selectByRunID(m.pendingSelectRunID) {
			m.pendingSelectRunID = ""
			if m.pendingEnterTerm {
				m.pendingEnterTerm = false
				m.mode = modeTerm
			}
		}
		return m, m.syncPTYAttachment()

	case daemonEventMsg:
		return m, tea.Batch(m.fetchStateCmd(), m.connectEventsCmd())

	case eventWSErrMsg:
		// Retry events WS after short backoff.
		return m, tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
			conn, err := m.client.ConnectEvents()
			if err != nil {
				return eventWSErrMsg{err: err}
			}
			var ev protocol.DaemonEvent
			if err := conn.ReadJSON(&ev); err != nil {
				_ = conn.Close()
				return eventWSErrMsg{err: err}
			}
			_ = conn.Close()
			return daemonEventMsg{ev: ev}
		})

	case ptyConnectedMsg:
		// Discard stale connection if generation moved on (prevents leaked/concurrent WS reads).
		if msg.gen != m.ptyGen || msg.runID != m.attachedRunID {
			if msg.conn != nil {
				_ = msg.conn.Close()
			}
			return m, nil
		}
		m.ptyConnecting = false
		if msg.err != nil {
			m.ptyDead = true
			if m.mode == modeTerm {
				m.mode = modeNav
				m.resizeViewportAndPTY()
			}
			bindLine := ""
			if r, ok := m.findRun(msg.runID); ok && r.SessionID != "" {
				bindLine = mutedStyle.Render("已绑定 Session: ") + keyHintStyle.Render(r.SessionID) + "\n\n"
			}
			m.ptyScreen = headerTitleStyle.Render("会话当前处于休眠状态") + "\n\n" +
				bindLine +
				softTextStyle.Render("按 ") + keyHintStyle.Render("Enter") + softTextStyle.Render(" 或直接点击此处，精准恢复绑定对话并进入终端") + "\n" +
				mutedStyle.Render("按 r 可更换绑定的历史 Session 或开启空白新对话")
			return m, nil
		}
		m.ptyDead = false
		if m.ptyConn != nil {
			_ = m.ptyConn.Close()
		}
		m.ptyConn = msg.conn
		return m, readPTYFrameCmd(msg.runID, msg.gen, msg.conn)

	case ptyScreenMsg:
		if msg.gen != m.ptyGen || msg.runID != m.attachedRunID || m.ptyConn == nil {
			return m, nil
		}
		if msg.screen != "" {
			if m.termSel.dragging {
				// Keep visible screen frozen during active mouse drag; flush on release.
				m.ptyDeferredScreen = msg.screen
				m.ptyDeferredLinks = msg.links
			} else {
				m.ptyScreen = msg.screen
				m.ptyDeferredScreen = ""
				m.ptyLinks = msg.links
				m.ptyDeferredLinks = nil
			}
			m.ptyScrollOffset = msg.scrollOffset
			m.ptyScrollMax = msg.scrollMax
			m.ptyCursor = msg.cursor
		}
		return m, readPTYFrameCmd(msg.runID, msg.gen, m.ptyConn)

	case ptyClosedMsg:
		if msg.gen != m.ptyGen || msg.runID != m.attachedRunID {
			return m, nil
		}
		m.ptyConnecting = false
		m.ptyDead = true
		if m.ptyConn != nil {
			_ = m.ptyConn.Close()
			m.ptyConn = nil
		}
		if m.shellMode && msg.runID == m.shellID {
			if strings.HasPrefix(msg.runID, "install-") {
				// Keep the full installer output visible on screen so the user can inspect
				// whether installation succeeded or failed before dismissing.
				m.mode = modeNav
				m.resizeViewportAndPTY()
				m.toast = "安装进程已结束 — 请查看上方日志确认结果（按 Esc/Enter 返回，按 , 打开控制中心）"
				return m, m.fetchStateCmd()
			}
			m.shellMode = false
			m.ptyDead = false
			m.attachedRunID = ""
			m.toast = "Terminal 已退出，已切回 Agent 视图"
			if m.currentRunID() == "" {
				m.mode = modeNav
				m.resizeViewportAndPTY()
			}
			return m, m.syncPTYAttachment()
		}
		if m.mode == modeTerm {
			m.mode = modeNav
			m.resizeViewportAndPTY()
		}
		hint := badgeWarnStyle.Render("会话已退出") + "  " +
			softTextStyle.Render("按 ") + keyHintStyle.Render("Enter") + softTextStyle.Render(" 或点击此处自动恢复上次对话  ·  按 ") +
			keyHintStyle.Render("r") + softTextStyle.Render(" 选择其他 Session")
		if strings.TrimSpace(m.ptyScreen) == "" {
			m.ptyScreen = hint
		} else {
			m.ptyScreen = strings.TrimRight(m.ptyScreen, "\n") + "\n\n" + hint
		}
		m.toast = "会话已退出 — 按 Enter 自动恢复，或按 r 选择 Session"
		return m, m.fetchStateCmd()

	case diffLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			return m, nil
		}
		m.diffData = &msg.diff
		if m.diffRunIdx >= len(msg.diff.Runs) {
			m.diffRunIdx = 0
		}
		m.refreshDiffViewport()
		m.mode = modeDiff
		return m, nil

	case runSessionsLoadedMsg:
		if m.modal == modalResume && msg.runID == m.resumeRunID {
			m.resumeLoading = false
			if msg.err != nil {
				m.errMsg = "读取历史 Session 失败: " + msg.err.Error()
				return m, nil
			}
			m.resumeSessions = msg.sessions
			m.resumeCursor = 0
			m.resumeScroll = 0
			if r, ok := m.findRun(msg.runID); ok && r.SessionID != "" {
				for i, s := range msg.sessions {
					if s.ID == r.SessionID {
						m.resumeCursor = i
						break
					}
				}
			}
			m.syncResumeInputFocus()
		}
		return m, nil

	case shellReadyMsg:
		m.loading = false
		if msg.err != nil {
			m.errMsg = "打开 Terminal 失败: " + msg.err.Error()
			return m, nil
		}
		m.shellMode = true
		m.shellID = msg.resp.ShellID
		m.shellDir = msg.resp.Dir
		m.shellBin = msg.resp.Shell
		m.mode = modeTerm
		m.ptyDead = false
		m.attachedRunID = ""
		return m, m.attachPTYTarget(m.shellID)

	case installStartedMsg:
		m.loading = false
		if msg.err != nil {
			m.errMsg = "打开安装终端失败: " + msg.err.Error()
			m.mode = modeNav
			return m, nil
		}
		m.shellMode = true
		m.shellID = msg.resp.SessionID
		m.shellDir = "install " + msg.resp.Name
		m.shellBin = "installer"
		if msg.resp.Running {
			m.mode = modeTerm
			if msg.resp.Reattached {
				m.toast = "已回到 " + msg.resp.Name + " 安装界面（实时输出中，Ctrl+] 可切回导航）"
			} else {
				m.toast = "正在安装 " + msg.resp.Name + "（实时输出中，切走后可按 , → 1 随时返回）"
			}
		} else {
			m.mode = modeNav
			m.toast = "正在查看 " + msg.resp.Name + " 上次安装日志（按 Esc/Enter 返回）"
		}
		m.ptyDead = false
		m.attachedRunID = ""
		return m, m.attachPTYTarget(m.shellID)

	case proxyTestedMsg:
		if msg.err != nil {
			m.proxyTestMsg = errorStyle.Render("✗ 测试失败: " + msg.err.Error())
		} else if msg.res.Reachable {
			m.proxyTestMsg = okStyle.Render(fmt.Sprintf("✓ 代理连通正常：%s (%dms)", msg.res.Endpoint, msg.res.LatencyMs))
		} else {
			m.proxyTestMsg = errorStyle.Render(fmt.Sprintf("✗ 代理不可达 (%s): %s", msg.res.Endpoint, msg.res.Error))
		}
		return m, nil

	case mobileStatusMsg:
		m.loading = false
		if msg.err != nil {
			m.errMsg = "手机同步服务错误: " + msg.err.Error()
			return m, nil
		}
		m.snap.Mobile = msg.st
		if m.mobileSessionCursor >= len(msg.st.Sessions) {
			m.mobileSessionCursor = max(0, len(msg.st.Sessions)-1)
		}
		switch {
		case msg.toast != "":
			m.toast = msg.toast
		case msg.st.Running:
			m.toast = "手机局域网同步已启动: " + msg.st.SelectedIP
		default:
			m.toast = "已停止手机局域网同步服务"
		}
		return m, nil

	case actionResultMsg:
		m.loading = false
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			return m, nil
		}
		m.errMsg = ""
		if msg.toast != "" {
			m.toast = msg.toast
		}
		if msg.closeModal {
			m.modal = modalNone
			m.mode = modeNav
		}
		if msg.exitDiff {
			m.diffData = nil
			m.mode = modeNav
		}
		var cmds []tea.Cmd
		if msg.reloadState {
			cmds = append(cmds, func() tea.Msg {
				snap, err := m.client.State()
				return stateLoadedMsg{snap: snap, err: err}
			})
		}
		if msg.selectRunID != "" {
			if !m.selectByRunID(msg.selectRunID) {
				m.pendingSelectRunID = msg.selectRunID
				m.pendingEnterTerm = msg.enterTerm
			}
		}
		if msg.reattachRunID != "" {
			if m.ptyConn != nil {
				_ = m.ptyConn.Close()
				m.ptyConn = nil
			}
			m.ptyConnecting = false
			m.ptyDead = false
			m.ptyGen++
			m.attachedRunID = ""
			if m.selectByRunID(msg.reattachRunID) {
				if msg.enterTerm {
					m.mode = modeTerm
				}
				if attachCmd := m.syncPTYAttachment(); attachCmd != nil {
					cmds = append(cmds, attachCmd)
				}
			} else {
				// Run not in local snapshot yet; stateLoadedMsg will select, enter TERM, and attach once.
				m.pendingSelectRunID = msg.reattachRunID
				m.pendingEnterTerm = msg.enterTerm
			}
		}
		return m, tea.Batch(cmds...)

	case termEscFlushMsg:
		if m.termEscPending && m.termEscSeq == msg.seq {
			m.termEscPending = false
			if m.mode == modeTerm && m.ptyConn != nil {
				_ = daemon.SendPTYInput(m.ptyConn, []byte{0x1b})
			}
		}
		return m, nil

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	// Delegate remaining messages to active modal inputs.
	if m.mode == modeModal {
		return m.updateModalNonKey(msg)
	}
	return m, nil
}

func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.mode == modeModal {
		if m.modal == modalAddProject && !m.manualPath {
			switch msg.Button {
			case tea.MouseButtonWheelUp:
				m.picker.moveUp()
			case tea.MouseButtonWheelDown:
				m.picker.moveDown()
			}
		} else if m.modal == modalResume && m.modalField == 0 {
			total := m.resumeTotalItems()
			if total > 0 {
				switch msg.Button {
				case tea.MouseButtonWheelUp:
					if m.resumeCursor > 0 {
						m.resumeCursor--
						m.syncResumeInputFocus()
					}
				case tea.MouseButtonWheelDown:
					if m.resumeCursor < total-1 {
						m.resumeCursor++
						m.syncResumeInputFocus()
					}
				}
			}
		} else if m.modal == modalPreview {
			var cmd tea.Cmd
			m.previewViewport, cmd = m.previewViewport.Update(msg)
			return m, cmd
		}
		return m, nil
	}

	sidebarW := m.sidebarWidth()
	cols, rows := m.ptyDimensions()
	originX := sidebarW + 2
	originY := 3

	// Handle active drag-to-select anywhere on screen (even if cursor crosses borders).
	if m.termSel.dragging {
		col := clampInt(msg.X-originX, 0, max(0, cols-1))
		row := clampInt(msg.Y-originY, 0, max(0, rows-1))
		switch msg.Action {
		case tea.MouseActionMotion:
			m.termSel.endX = col
			m.termSel.endY = row
			if col != m.termSel.startX || row != m.termSel.startY {
				m.termSel.active = true
			}
			return m, nil
		case tea.MouseActionRelease:
			m.termSel.dragging = false
			m.termSel.endX = col
			m.termSel.endY = row
			wasDrag := m.termSel.active || col != m.termSel.startX || row != m.termSel.startY
			if wasDrag {
				m.termSel.active = true
				screenLines := strings.Split(m.ptyScreen, "\n")
				if len(screenLines) > rows {
					screenLines = screenLines[:rows]
				}
				if selected := extractSelectedText(screenLines, m.termSel); selected != "" {
					_ = copyToClipboard(selected)
					n := len([]rune(selected))
					m.copyNotice = fmt.Sprintf("✓ 已复制 %d 字", n)
					m.copyNoticeAt = time.Now()
					m.toast = m.copyNotice
				}
				if m.ptyDeferredScreen != "" {
					m.ptyScreen = m.ptyDeferredScreen
					m.ptyDeferredScreen = ""
					m.ptyLinks = m.ptyDeferredLinks
					m.ptyDeferredLinks = nil
				}
				return m, nil
			}
			// Simple click without dragging -> check clickable link first (OSC 8 or plain URL), else focus TERM mode.
			m.termSel.clear()
			if m.ptyDeferredScreen != "" {
				m.ptyScreen = m.ptyDeferredScreen
				m.ptyDeferredScreen = ""
				m.ptyLinks = m.ptyDeferredLinks
				m.ptyDeferredLinks = nil
			}
			if linkURL := m.linkAt(col, row); linkURL != "" {
				if fp, startL, endL, isFile := parseFileLink(linkURL); isFile {
					m.openFilePreviewModal(fp, startL, endL)
					return m, nil
				}
				if err := openURLInBrowser(linkURL); err != nil {
					m.errMsg = "打开链接失败: " + err.Error()
				} else {
					host := linkURL
					if pu, err := url.Parse(linkURL); err == nil && pu.Host != "" {
						host = pu.Host
					}
					m.copyNotice = "↗ 已在浏览器打开 " + truncate(host, 28)
					m.copyNoticeAt = time.Now()
					m.toast = m.copyNotice
				}
				return m, nil
			}
			if m.shellMode {
				if m.ptyConn != nil {
					wasNav := m.mode != modeTerm
					m.mode = modeTerm
					if wasNav {
						m.resizeViewportAndPTY()
					}
				}
				return m, nil
			}
			if runID := m.currentRunID(); runID != "" {
				if m.ptyConn == nil {
					if m.ptyConnecting || m.loading {
						return m, nil
					}
					return m, m.autoResumeRunCmd(runID)
				}
				wasNav := m.mode != modeTerm
				m.mode = modeTerm
				if wasNav {
					m.resizeViewportAndPTY()
				}
				return m, m.ackIfWaitingCmd(runID)
			}
			return m, nil
		}
		return m, nil
	}

	// Clicking or scrolling in the left sidebar.
	if sidebarW > 0 && msg.X < sidebarW {
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			if len(m.nodes) > 0 && m.sel > 0 {
				m.sel--
				return m, m.syncPTYAttachment()
			}
		case tea.MouseButtonWheelDown:
			if len(m.nodes) > 0 && m.sel < len(m.nodes)-1 {
				m.sel++
				return m, m.syncPTYAttachment()
			}
		case tea.MouseButtonLeft:
			if msg.Action == tea.MouseActionPress {
				m.termSel.clear()
				wasTerm := m.mode != modeNav
				m.mode = modeNav
				if wasTerm {
					m.resizeViewportAndPTY()
				}
				// Row offset: 1 (top border) + 1 (title) + 1 (divider) = 3.
				row := msg.Y - 3 + m.sidebarScroll
				if row >= 0 && row < len(m.nodes) {
					m.shellMode = false
					clickedSame := row == m.sel
					m.sel = row
					n := m.nodes[row]
					if (n.kind == nodeProject || n.kind == nodeTask) && (clickedSame || msg.X <= 7) {
						m.toggleCurrentCollapse()
					}
					return m, m.syncPTYAttachment()
				}
			}
		}
		return m, nil
	}

	// Clicking or scrolling in the right main pane.
	if m.mode == modeDiff {
		var cmd tea.Cmd
		m.diffViewport, cmd = m.diffViewport.Update(msg)
		return m, cmd
	}

	relX := msg.X - originX
	relY := msg.Y - originY
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.termSel.clear()
		if m.ptyConn != nil {
			_ = daemon.SendPTYScroll(m.ptyConn, 3, relX, relY)
		}
		return m, nil
	case tea.MouseButtonWheelDown:
		m.termSel.clear()
		if m.ptyConn != nil {
			_ = daemon.SendPTYScroll(m.ptyConn, -3, relX, relY)
		}
		return m, nil
	case tea.MouseButtonLeft:
		if msg.Action == tea.MouseActionPress {
			if m.shellMode || m.currentRunID() != "" {
				if relY >= 0 && relX >= 0 && m.ptyScreen != "" {
					// Begin potential drag-to-select; if released without moving, enters TERM mode.
					col := clampInt(relX, 0, max(0, cols-1))
					row := clampInt(relY, 0, max(0, rows-1))
					m.termSel = textSel{
						dragging: true,
						active:   false,
						startX:   col,
						startY:   row,
						endX:     col,
						endY:     row,
					}
					return m, nil
				}
				m.termSel.clear()
				if m.shellMode {
					if m.ptyConn != nil {
						m.mode = modeTerm
						m.resizeViewportAndPTY()
					}
					return m, nil
				}
				runID := m.currentRunID()
				if m.ptyConn == nil {
					if m.ptyConnecting || m.loading {
						return m, nil
					}
					return m, m.autoResumeRunCmd(runID)
				}
				m.mode = modeTerm
				m.resizeViewportAndPTY()
				return m, m.ackIfWaitingCmd(runID)
			}
		}
	case tea.MouseButtonMiddle:
		if msg.Action == tea.MouseActionPress && m.ptyConn != nil {
			m.pasteFromLocalClipboard()
			return m, nil
		}
	case tea.MouseButtonRight:
		if msg.Action == tea.MouseActionPress {
			if m.termSel.active {
				screenLines := strings.Split(m.ptyScreen, "\n")
				if len(screenLines) > rows {
					screenLines = screenLines[:rows]
				}
				if selected := extractSelectedText(screenLines, m.termSel); selected != "" {
					_ = copyToClipboard(selected)
					m.copyNotice = fmt.Sprintf("✓ 已复制 %d 字", len([]rune(selected)))
					m.copyNoticeAt = time.Now()
					m.toast = m.copyNotice
				}
				return m, nil
			}
			if m.ptyConn != nil {
				m.pasteFromLocalClipboard()
			}
			return m, nil
		}
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Global Ctrl+C in NAV/MODAL/DIFF quits cleanly (in TERM mode, Ctrl+C goes to the agent!).
	if m.mode != modeTerm && msg.String() == "ctrl+c" {
		m.cleanupConns()
		return m, tea.Quit
	}

	switch m.mode {
	case modeTerm:
		return m.handleTermKey(msg)
	case modeModal:
		return m.handleModalKey(msg)
	case modeDiff:
		return m.handleDiffKey(msg)
	default:
		return m.handleNavKey(msg)
	}
}

func (m Model) escapeKey() string {
	k := strings.ToLower(strings.TrimSpace(m.snap.EscapeKey))
	if k == "" {
		return "ctrl+]"
	}
	return k
}

func (m Model) escapeKeyLabel() string {
	k := m.escapeKey()
	if k == "ctrl+]" {
		return "Ctrl+]"
	}
	if strings.HasPrefix(k, "ctrl+") && len(k) > 5 {
		return "Ctrl+" + strings.ToUpper(k[5:])
	}
	return k
}

func (m Model) handleTermKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Universal escape key (default Ctrl+], configurable via ~/.aio/config.toml escape_key)
	// returns cleanly to NAV mode without hijacking any Alt/Option or Agent CLI shortcuts.
	escKey := m.escapeKey()
	if msg.String() == escKey || (escKey == "ctrl+]" && msg.Type == tea.KeyCtrlCloseBracket) {
		m.termEscPending = false
		m.termEscSeq++
		m.mode = modeNav
		m.resizeViewportAndPTY()
		return m, nil
	}

	// Ctrl+\ directly toggles the workspace Shell terminal from inside TERM mode
	// (Agent TERM <-> Shell TERM) without detouring through NAV mode.
	if msg.Type == tea.KeyCtrlBackslash || msg.String() == "ctrl+\\" {
		m.termEscPending = false
		m.termEscSeq++
		return m, m.toggleShellCmd()
	}

	// Buffered Double-Esc (two Esc presses within 220ms) exits TERM -> NAV WITHOUT
	// sending any Esc byte to the Agent (so active generation is NEVER interrupted).
	// A single Esc flushes to the Agent after 220ms (or immediately before the next key).
	if msg.Type == tea.KeyEsc {
		if m.termEscPending {
			m.termEscPending = false
			m.termEscSeq++
			m.mode = modeNav
			m.resizeViewportAndPTY()
			return m, nil
		}
		m.termEscPending = true
		m.termEscSeq++
		seq := m.termEscSeq
		return m, tea.Tick(220*time.Millisecond, func(time.Time) tea.Msg {
			return termEscFlushMsg{seq: seq}
		})
	}

	// Emergency SSH native mouse toggle via F12 (zero conflict with macOS Option or Agent CLIs).
	if msg.String() == "f12" {
		m.termEscPending = false
		m.termEscSeq++
		return m.toggleMouseMode()
	}

	// Flush any pending single Esc before forwarding the next regular key (0ms Esc+<key> lag).
	if m.termEscPending {
		m.termEscPending = false
		m.termEscSeq++
		if m.ptyConn != nil {
			_ = daemon.SendPTYInput(m.ptyConn, []byte{0x1b})
		}
	}

	if m.ptyConn == nil {
		if runID := m.currentRunID(); runID != "" {
			if msg.String() == "r" {
				return m, m.openResumeModal(runID)
			}
			if msg.String() == "enter" && !m.ptyConnecting && !m.loading {
				return m, m.autoResumeRunCmd(runID)
			}
		}
		return m, nil
	}

	// Native terminal bracketed paste (Ctrl+Shift+V / Cmd+V / Shift+Insert / Shift+right-click).
	if msg.Paste {
		m.termSel.clear()
		if len(msg.Runes) > 0 {
			_ = daemon.SendPTYPaste(m.ptyConn, string(msg.Runes))
		}
		return m, nil
	}

	m.termSel.clear()
	if raw := encodeKeyToPTY(msg); len(raw) > 0 {
		_ = daemon.SendPTYInput(m.ptyConn, raw)
	}
	return m, nil
}

func (m Model) handleNavKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.errMsg = ""

	// Allow direct bracketed paste (Ctrl+Shift+V / Cmd+V / Shift+Insert / Shift+right-click)
	// even while focused in NAV mode: auto-enter TERM mode and forward straight to the active PTY.
	if msg.Paste {
		if m.ptyConn != nil && len(msg.Runes) > 0 {
			m.termSel.clear()
			m.mode = modeTerm
			m.resizeViewportAndPTY()
			_ = daemon.SendPTYPaste(m.ptyConn, string(msg.Runes))
			if runID := m.currentRunID(); !m.shellMode && runID != "" {
				return m, m.ackIfWaitingCmd(runID)
			}
		}
		return m, nil
	}

	switch msg.String() {
	case "q":
		m.cleanupConns()
		return m, tea.Quit

	case "j", "down":
		if len(m.nodes) > 0 && m.sel < len(m.nodes)-1 {
			m.shellMode = false
			m.sel++
			return m, m.syncPTYAttachment()
		}
	case "k", "up":
		if len(m.nodes) > 0 && m.sel > 0 {
			m.shellMode = false
			m.sel--
			return m, m.syncPTYAttachment()
		}

	case " ":
		if m.toggleCurrentCollapse() {
			return m, m.syncPTYAttachment()
		}
		return m, nil

	case "left", "h":
		if m.collapseOrJumpParent() {
			return m, m.syncPTYAttachment()
		}
		return m, nil

	case "right", "l":
		if len(m.nodes) > 0 && m.sel < len(m.nodes) {
			n := m.nodes[m.sel]
			if n.kind == nodeProject || n.kind == nodeTask {
				if m.expandOrStepChild() {
					return m, m.syncPTYAttachment()
				}
				return m, nil
			}
			// On a Run node, right/l enters TERM mode (auto-resuming if dormant).
			if n.runID != "" {
				if m.ptyConn == nil {
					if m.ptyConnecting || m.loading {
						return m, nil
					}
					return m, m.autoResumeRunCmd(n.runID)
				}
				m.mode = modeTerm
				m.resizeViewportAndPTY()
				return m, m.ackIfWaitingCmd(n.runID)
			}
		}
		return m, nil

	case "esc":
		if m.shellMode && m.ptyDead {
			m.shellMode = false
			m.ptyDead = false
			m.attachedRunID = ""
			return m, m.syncPTYAttachment()
		}
		return m, nil

	case "enter":
		if m.shellMode {
			if m.ptyDead {
				m.shellMode = false
				m.ptyDead = false
				m.attachedRunID = ""
				return m, m.syncPTYAttachment()
			}
			if m.ptyConn != nil {
				m.mode = modeTerm
				m.resizeViewportAndPTY()
				return m, nil
			}
		}
		if len(m.nodes) > 0 && m.sel < len(m.nodes) {
			n := m.nodes[m.sel]
			if n.kind == nodeProject || n.kind == nodeTask {
				m.toggleCurrentCollapse()
				return m, m.syncPTYAttachment()
			}
		}
		if runID := m.currentRunID(); runID != "" {
			if m.ptyConn == nil {
				if m.ptyConnecting || m.loading {
					return m, nil
				}
				return m, m.autoResumeRunCmd(runID)
			}
			m.mode = modeTerm
			m.resizeViewportAndPTY()
			return m, m.ackIfWaitingCmd(runID)
		}
		return m, nil

	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		m.shellMode = false
		idx := int(msg.String()[0] - '1')
		if cmd := m.quickSwitchTaskRun(idx); cmd != nil {
			return m, cmd
		}
		return m, nil

	case "t", "ctrl+\\":
		return m, m.toggleShellCmd()

	case "z":
		m.fullscreen = !m.fullscreen
		m.resizeViewportAndPTY()
		return m, tea.ClearScreen

	case "ctrl+l":
		return m, tea.ClearScreen

	case "f12":
		return m.toggleMouseMode()

	case "y":
		m.copyCurrentSelectionOrScreen()
		return m, nil

	case "pgup":
		if m.ptyConn != nil {
			_ = daemon.SendPTYScroll(m.ptyConn, 10, 0, 0)
		}
		return m, nil

	case "pgdown":
		if m.ptyConn != nil {
			_ = daemon.SendPTYScroll(m.ptyConn, -10, 0, 0)
		}
		return m, nil

	case "end":
		if m.ptyConn != nil {
			_ = daemon.SendPTYScroll(m.ptyConn, 0, 0, 0)
		}
		return m, nil

	case "p":
		m.openAddProjectModal()
		return m, nil

	case "n":
		if m.currentProjectID() == "" {
			m.errMsg = "请先按 p 添加一个本地工作目录"
			return m, nil
		}
		m.openNewTaskModal(false)
		return m, nil

	case "d":
		taskID := m.currentTaskID()
		if taskID == "" {
			m.errMsg = "请先选中一个 Task 或 Run 查看 Diff 对比"
			return m, nil
		}
		m.loading = true
		m.toast = "正在计算各 Agent Worktree Diff..."
		return m, m.loadDiffCmd(taskID)

	case "a":
		runID := m.currentRunID()
		taskID := m.currentTaskID()
		if runID == "" || taskID == "" {
			m.errMsg = "请先选中一个要采纳（Adopt）的 Agent Run"
			return m, nil
		}
		m.openAdoptModal(taskID, runID)
		return m, nil

	case "w":
		return m, m.jumpToNextWaiting()

	case "r":
		runID := m.currentRunID()
		if runID == "" {
			return m, nil
		}
		return m, m.openResumeModal(runID)

	case "s":
		runID := m.currentRunID()
		if runID == "" {
			return m, nil
		}
		return m, func() tea.Msg {
			err := m.client.StopRun(runID)
			return actionResultMsg{
				toast:       "已停止会话",
				err:         err,
				reloadState: true,
			}
		}

	case "+", "=":
		taskID := m.currentTaskID()
		if taskID == "" {
			m.errMsg = "请先选中一个已有任务再追加 Agent"
			return m, nil
		}
		m.openAddRunsModal(taskID)
		return m, nil

	case "x", "delete":
		if len(m.nodes) == 0 || m.sel >= len(m.nodes) {
			return m, nil
		}
		node := m.nodes[m.sel]
		switch node.kind {
		case nodeProject:
			m.deleteKind = nodeProject
			m.deleteTarget = node.projectID
			m.deleteLabel = "工作目录 " + node.label + "（及其下所有任务）"
		case nodeTask:
			m.deleteKind = nodeTask
			m.deleteTarget = node.taskID
			m.deleteLabel = "任务 " + m.taskTitle(node.taskID) + "（停止其下所有 Agent 并清理 Worktree/分支）"
		case nodeRun:
			m.deleteKind = nodeRun
			m.deleteTarget = node.runID
			m.deleteLabel = "Agent 会话 " + node.label + "（若为任务下最后一个将一并移除空任务）"
		}
		m.deleteCleanSession = true
		m.modal = modalConfirmDelete
		m.mode = modeModal
		return m, nil

	case "e", "f2":
		taskID := m.currentTaskID()
		if taskID == "" {
			m.errMsg = "请先选中一个任务（或其下的 Agent）再重命名"
			return m, nil
		}
		m.openRenameTaskModal(taskID)
		return m, nil

	case ",":
		m.openControlCenterModal()
		return m, m.fetchStateCmd()

	case "?":
		m.openHelpModal()
		return m, nil
	}

	return m, nil
}

func (m Model) handleDiffKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "d", "q":
		m.mode = modeNav
		return m, nil
	case "left", "h":
		if m.diffData != nil && len(m.diffData.Runs) > 0 {
			m.diffRunIdx = (m.diffRunIdx - 1 + len(m.diffData.Runs)) % len(m.diffData.Runs)
			m.refreshDiffViewport()
		}
		return m, nil
	case "right", "l", "tab":
		if m.diffData != nil && len(m.diffData.Runs) > 0 {
			m.diffRunIdx = (m.diffRunIdx + 1) % len(m.diffData.Runs)
			m.refreshDiffViewport()
		}
		return m, nil
	case "r":
		if m.diffData != nil {
			m.loading = true
			return m, m.loadDiffCmd(m.diffData.TaskID)
		}
		return m, nil
	case "a":
		if m.diffData != nil && len(m.diffData.Runs) > 0 {
			winner := m.diffData.Runs[m.diffRunIdx]
			m.openAdoptModal(m.diffData.TaskID, winner.RunID)
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.diffViewport, cmd = m.diffViewport.Update(msg)
	return m, cmd
}

func (m *Model) addedProjectPaths() map[string]bool {
	added := make(map[string]bool, len(m.snap.Projects))
	for _, p := range m.snap.Projects {
		added[p.RepoPath] = true
	}
	return added
}

func (m *Model) openAddProjectModal() {
	m.modal = modalAddProject
	m.mode = modeModal
	m.manualPath = false
	m.creatingDir = false
	m.mkdirInitGit = false
	m.pathInput.Blur()
	m.mkdirInput.SetValue("")
	m.mkdirInput.Blur()
	m.picker = newDirPicker(m.cwd, m.addedProjectPaths())
}

func (m *Model) openNewTaskModal(compare bool) {
	m.modal = modalNewTask
	m.modalIsComp = compare
	m.mode = modeModal
	m.modalField = 0
	m.titleInput.SetValue("")
	m.promptInput.SetValue("")
	m.magpieInput.SetValue("")
	m.titleInput.Focus()
	m.promptInput.Blur()
	m.magpieInput.Blur()
	m.permIdx = 0
	m.agentCursor = 0
	m.agentChecked = make(map[string]bool)
	m.workModeManual = false

	picked := 0
	for _, a := range m.snap.Agents {
		if !a.Available {
			continue
		}
		if compare {
			if picked < 2 {
				m.agentChecked[a.Name] = true
				picked++
			}
		} else {
			if picked == 0 {
				m.agentChecked[a.Name] = true
				picked++
			}
		}
	}
	// Smart default work mode:
	// - Single Git repo OR multi-repo workspace + `c` (compare): isolated (per-agent worktree)
	// - Single Git repo OR multi-repo workspace + `n` (normal task): shared (1 task = 1 worktree, seamless A->B->C quota handoff!)
	// - Plain non-Git workspace (zero git repos): local
	if proj, ok := m.findProject(m.currentProjectID()); ok && proj.SupportsWorktree() {
		if compare {
			m.workModeIdx = 2 // isolated
		} else {
			m.workModeIdx = 0 // shared
		}
	} else {
		m.workModeIdx = 1 // local
	}
}

func (m *Model) openAddRunsModal(taskID string) {
	m.modal = modalAddRuns
	m.mode = modeModal
	m.addRunsTaskID = taskID
	m.modalField = 0 // 0=Agents, 1=Prompt, 2=PermMode
	m.promptInput.SetValue("")
	m.magpieInput.SetValue("")
	m.promptInput.Blur()
	m.magpieInput.Blur()
	m.permIdx = 0
	m.agentCursor = 0
	m.agentChecked = make(map[string]bool)

	existing := make(map[string]bool)
	for _, r := range m.snap.Runs {
		if r.TaskID == taskID {
			existing[r.Agent] = true
		}
	}
	// Pre-check the first available agent not already in this task.
	picked := false
	for i, a := range m.snap.Agents {
		if a.Available && !existing[a.Name] {
			m.agentChecked[a.Name] = true
			m.agentCursor = i
			picked = true
			break
		}
	}
	if !picked {
		for i, a := range m.snap.Agents {
			if a.Available {
				m.agentChecked[a.Name] = true
				m.agentCursor = i
				break
			}
		}
	}
}

func (m *Model) openRenameTaskModal(taskID string) {
	m.modal = modalRenameTask
	m.mode = modeModal
	m.renameTaskID = taskID
	m.titleInput.SetValue(m.taskTitle(taskID))
	m.titleInput.Focus()
	m.titleInput.CursorEnd()
}

func (m *Model) openAdoptModal(taskID, runID string) {
	m.modal = modalAdopt
	m.mode = modeModal
	m.adoptTaskID = taskID
	m.adoptRunID = runID
	m.mergeIdx = 0 // Default: GitHub Squash and merge
	m.modalField = 0
	m.commitInput.SetValue("")
	m.commitInput.Blur()
}

func (m *Model) openResumeModal(runID string) tea.Cmd {
	m.modal = modalResume
	m.mode = modeModal
	m.resumeRunID = runID
	m.resumeCursor = 0
	m.resumeScroll = 0
	m.resumeSessions = nil
	m.resumeLoading = true
	m.modalField = 0
	if r, ok := m.findRun(runID); ok && r.SessionID != "" {
		m.sessionInput.SetValue(r.SessionID)
	} else {
		m.sessionInput.SetValue("")
	}
	m.sessionInput.Blur()
	return func() tea.Msg {
		list, err := m.client.ListRunSessions(runID)
		return runSessionsLoadedMsg{runID: runID, sessions: list, err: err}
	}
}

func (m Model) resumeTotalItems() int {
	// Existing discovered sessions + 3 action rows (fresh, manual id, cli picker).
	return len(m.resumeSessions) + 3
}

func (m Model) resumeActionAt(cursor int) string {
	idx := cursor - len(m.resumeSessions)
	switch idx {
	case 0:
		return "fresh"
	case 1:
		return "id"
	case 2:
		return "picker"
	default:
		return ""
	}
}

func (m *Model) openFilePreviewModal(filePath string, startLine, endLine int) {
	boxW := min(max(50, m.width-8), 116)
	boxH := max(12, m.height-6)
	vpW := max(40, boxW-6)
	vpH := max(6, boxH-8)

	data, err := loadFilePreview(filePath, startLine, endLine, vpW)
	if err != nil {
		m.errMsg = err.Error()
		return
	}
	m.errMsg = ""
	if m.mode != modeModal {
		m.previewReturnMode = m.mode
	}
	m.previewData = data
	vp := viewport.New(vpW, vpH)
	vp.SetContent(data.Content)
	if startLine > 1 {
		// Position referenced line nicely near the top third of the preview viewport.
		offset := max(0, startLine-1-vpH/4)
		vp.SetYOffset(offset)
	}
	m.previewViewport = vp
	m.modal = modalPreview
	m.mode = modeModal
}

func (m *Model) closePreviewModal() {
	m.modal = modalNone
	if m.previewReturnMode == modeTerm && m.ptyConn != nil {
		m.mode = modeTerm
	} else {
		m.mode = modeNav
	}
	m.resizeViewportAndPTY()
}

func (m Model) handleModalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "esc" {
		if m.modal == modalTools && m.toolsEditingPath {
			m.toolsEditingPath = false
			m.toolsPathInput.Blur()
			return m, nil
		}
		if m.modal == modalAddProject && m.creatingDir {
			m.creatingDir = false
			m.mkdirInput.Blur()
			return m, nil
		}
		if m.modal == modalPreview {
			m.closePreviewModal()
			return m, nil
		}
		m.modal = modalNone
		m.mode = modeNav
		return m, nil
	}

	switch m.modal {
	case modalAddProject:
		if m.creatingDir {
			switch msg.String() {
			case "tab":
				m.mkdirInitGit = !m.mkdirInitGit
				return m, nil
			case "enter":
				created, err := m.picker.createSubDir(m.mkdirInput.Value(), m.mkdirInitGit, m.addedProjectPaths())
				if err != nil {
					m.errMsg = err.Error()
					return m, nil
				}
				m.errMsg = ""
				m.creatingDir = false
				m.mkdirInput.SetValue("")
				m.mkdirInput.Blur()
				m.toast = "已新建文件夹: " + created
				return m, nil
			}
			var cmd tea.Cmd
			m.mkdirInput, cmd = m.mkdirInput.Update(msg)
			return m, cmd
		}
		if m.manualPath {
			switch msg.String() {
			case "tab":
				m.manualPath = false
				m.pathInput.Blur()
				return m, nil
			case "enter":
				repo := strings.TrimSpace(m.pathInput.Value())
				if repo == "" {
					m.errMsg = "工作目录路径不能为空"
					return m, nil
				}
				return m.submitAddProject(repo)
			}
			var cmd tea.Cmd
			m.pathInput, cmd = m.pathInput.Update(msg)
			return m, cmd
		}

		added := m.addedProjectPaths()
		if msg.Paste {
			pasted := strings.TrimSpace(string(msg.Runes))
			if pasted != "" {
				m.manualPath = true
				m.pathInput.SetValue(pasted)
				m.pathInput.Focus()
				m.pathInput.CursorEnd()
			}
			return m, nil
		}
		switch msg.String() {
		case "up", "k":
			m.picker.moveUp()
			return m, nil
		case "down", "j":
			m.picker.moveDown()
			return m, nil
		case "right", "l", "tab":
			m.picker.enterSelectedDir(added)
			return m, nil
		case "left", "h", "backspace":
			m.picker.goUp(added)
			return m, nil
		case "~":
			m.picker.goHome(added)
			return m, nil
		case ".":
			m.picker.toggleHidden(added)
			return m, nil
		case "/":
			m.manualPath = true
			m.pathInput.SetValue(m.picker.currentDir)
			m.pathInput.Focus()
			m.pathInput.CursorEnd()
			return m, nil
		case "N", "n":
			m.creatingDir = true
			m.mkdirInitGit = false
			m.mkdirInput.SetValue("")
			m.mkdirInput.Focus()
			return m, nil
		case "enter":
			entry, ok := m.picker.selected()
			if !ok {
				return m, nil
			}
			if entry.kind == dirEntryParent {
				m.picker.goUp(added)
				return m, nil
			}
			return m.submitAddProject(entry.path)
		}
		return m, nil

	case modalNewTask:
		// Fields: 0=Title, 1=Prompt, 2=Agents, 3=WorkMode, 4=PermMode [, 5=MagpieRoute if magpie available]
		totalFields := 5
		if m.snap.Magpie.Available {
			totalFields = 6
		}
		switch msg.String() {
		case "tab", "down", "j":
			if msg.String() == "j" && m.modalField != 2 {
				break
			}
			if m.modalField == 2 && (msg.String() == "down" || msg.String() == "j") && m.agentCursor < len(m.snap.Agents)-1 {
				m.agentCursor++
				return m, nil
			}
			m.modalField = (m.modalField + 1) % totalFields
			m.syncModalFocus()
			return m, nil
		case "shift+tab", "up", "k":
			if msg.String() == "k" && m.modalField != 2 {
				break
			}
			if m.modalField == 2 && (msg.String() == "up" || msg.String() == "k") && m.agentCursor > 0 {
				m.agentCursor--
				return m, nil
			}
			m.modalField = (m.modalField + totalFields - 1) % totalFields
			m.syncModalFocus()
			return m, nil
		case " ":
			if m.modalField == 2 && len(m.snap.Agents) > 0 {
				a := m.snap.Agents[m.agentCursor]
				if !a.Available {
					return m, nil
				}
				m.agentChecked[a.Name] = !m.agentChecked[a.Name]
				return m, nil
			}
			if m.modalField == 3 {
				if proj, ok := m.findProject(m.currentProjectID()); ok && proj.SupportsWorktree() {
					m.workModeIdx = (m.workModeIdx + 1) % len(m.workModeOpts)
					m.workModeManual = true
				}
				return m, nil
			}
			if m.modalField == 4 {
				m.permIdx = (m.permIdx + 1) % len(m.permOptions)
				return m, nil
			}
		case "left", "right":
			if m.modalField == 3 {
				if proj, ok := m.findProject(m.currentProjectID()); ok && proj.SupportsWorktree() {
					if msg.String() == "left" {
						m.workModeIdx = (m.workModeIdx - 1 + len(m.workModeOpts)) % len(m.workModeOpts)
					} else {
						m.workModeIdx = (m.workModeIdx + 1) % len(m.workModeOpts)
					}
					m.workModeManual = true
				}
				return m, nil
			}
			if m.modalField == 4 {
				if msg.String() == "left" {
					m.permIdx = (m.permIdx - 1 + len(m.permOptions)) % len(m.permOptions)
				} else {
					m.permIdx = (m.permIdx + 1) % len(m.permOptions)
				}
				return m, nil
			}
		case "enter":
			if m.modalField < 2 {
				m.modalField++
				m.syncModalFocus()
				return m, nil
			}
			return m.submitNewTask()
		}

		var cmd tea.Cmd
		if m.modalField == 0 {
			m.titleInput, cmd = m.titleInput.Update(msg)
		} else if m.modalField == 1 {
			m.promptInput, cmd = m.promptInput.Update(msg)
		} else if m.modalField == 5 {
			m.magpieInput, cmd = m.magpieInput.Update(msg)
		}
		return m, cmd

	case modalAdopt:
		// Fields: 0=MergeStrategy, 1=CommitMsg
		switch msg.String() {
		case "tab":
			m.modalField = (m.modalField + 1) % 2
			if m.modalField == 1 {
				m.commitInput.Focus()
			} else {
				m.commitInput.Blur()
			}
			return m, nil
		case "up", "k":
			if m.modalField == 0 {
				m.mergeIdx = (m.mergeIdx - 1 + len(m.mergeOptions)) % len(m.mergeOptions)
				return m, nil
			}
		case "down", "j":
			if m.modalField == 0 {
				m.mergeIdx = (m.mergeIdx + 1) % len(m.mergeOptions)
				return m, nil
			}
		case "enter":
			taskID := m.adoptTaskID
			runID := m.adoptRunID
			strat := m.mergeOptions[m.mergeIdx]
			commitMsg := strings.TrimSpace(m.commitInput.Value())
			m.loading = true
			return m, func() tea.Msg {
				resp, err := m.client.AdoptTask(taskID, protocol.AdoptTaskRequest{
					WinnerRunID: runID,
					Strategy:    strat,
					CommitMsg:   commitMsg,
				})
				if err != nil {
					return actionResultMsg{err: err}
				}
				return actionResultMsg{
					toast:       fmt.Sprintf("已通过 %s 合并回 %s，并清理 %d 个 Worktree", resp.Strategy, resp.TargetBranch, resp.CleanedWorktrees),
					closeModal:  true,
					exitDiff:    true,
					reloadState: true,
				}
			}
		}
		if m.modalField == 1 {
			var cmd tea.Cmd
			m.commitInput, cmd = m.commitInput.Update(msg)
			return m, cmd
		}
		return m, nil

	case modalAddRuns:
		// Fields: 0=Agents, 1=Prompt, 2=PermMode [, 3=MagpieRoute if magpie available]
		totalFields := 3
		if m.snap.Magpie.Available {
			totalFields = 4
		}
		syncAddRunsFocus := func() {
			if m.modalField == 1 {
				m.promptInput.Focus()
			} else {
				m.promptInput.Blur()
			}
			if m.modalField == 3 {
				m.magpieInput.Focus()
			} else {
				m.magpieInput.Blur()
			}
		}
		switch msg.String() {
		case "tab", "down", "j":
			if msg.String() == "j" && m.modalField != 0 {
				break
			}
			if m.modalField == 0 && (msg.String() == "down" || msg.String() == "j") && m.agentCursor < len(m.snap.Agents)-1 {
				m.agentCursor++
				return m, nil
			}
			m.modalField = (m.modalField + 1) % totalFields
			syncAddRunsFocus()
			return m, nil
		case "shift+tab", "up", "k":
			if msg.String() == "k" && m.modalField != 0 {
				break
			}
			if m.modalField == 0 && (msg.String() == "up" || msg.String() == "k") && m.agentCursor > 0 {
				m.agentCursor--
				return m, nil
			}
			m.modalField = (m.modalField + totalFields - 1) % totalFields
			syncAddRunsFocus()
			return m, nil
		case " ":
			if m.modalField == 0 && len(m.snap.Agents) > 0 {
				a := m.snap.Agents[m.agentCursor]
				if a.Available {
					m.agentChecked[a.Name] = !m.agentChecked[a.Name]
				}
				return m, nil
			}
			if m.modalField == 2 {
				m.permIdx = (m.permIdx + 1) % len(m.permOptions)
				return m, nil
			}
		case "left", "right":
			if m.modalField == 2 {
				if msg.String() == "left" {
					m.permIdx = (m.permIdx - 1 + len(m.permOptions)) % len(m.permOptions)
				} else {
					m.permIdx = (m.permIdx + 1) % len(m.permOptions)
				}
				return m, nil
			}
		case "enter":
			return m.submitAddRuns()
		}
		if m.modalField == 1 {
			var cmd tea.Cmd
			m.promptInput, cmd = m.promptInput.Update(msg)
			return m, cmd
		}
		if m.modalField == 3 {
			var cmd tea.Cmd
			m.magpieInput, cmd = m.magpieInput.Update(msg)
			return m, cmd
		}
		return m, nil

	case modalRenameTask:
		if msg.String() == "enter" {
			newTitle := strings.TrimSpace(m.titleInput.Value())
			if newTitle == "" {
				m.errMsg = "任务名称不能为空"
				return m, nil
			}
			taskID := m.renameTaskID
			m.loading = true
			return m, func() tea.Msg {
				_, err := m.client.RenameTask(taskID, newTitle)
				return actionResultMsg{
					toast:       "任务已重命名为 " + newTitle,
					err:         err,
					closeModal:  true,
					reloadState: true,
				}
			}
		}
		var cmd tea.Cmd
		m.titleInput, cmd = m.titleInput.Update(msg)
		return m, cmd

	case modalConfirmDelete:
		if msg.String() == " " {
			m.deleteCleanSession = !m.deleteCleanSession
			return m, nil
		}
		if msg.String() == "y" || msg.String() == "enter" {
			kind := m.deleteKind
			target := m.deleteTarget
			cleanSess := m.deleteCleanSession
			m.loading = true
			return m, func() tea.Msg {
				var err error
				switch kind {
				case nodeProject:
					err = m.client.DeleteProject(target, cleanSess)
				case nodeTask:
					err = m.client.DeleteTask(target, cleanSess)
				case nodeRun:
					err = m.client.DeleteRun(target, cleanSess)
				}
				toast := "已杀掉进程并完成删除清理"
				if cleanSess {
					toast = "已杀掉进程并彻底清理对应 Session"
				}
				return actionResultMsg{
					toast:       toast,
					err:         err,
					closeModal:  true,
					reloadState: true,
				}
			}
		}
		if msg.String() == "n" {
			m.modal = modalNone
			m.mode = modeNav
		}
		return m, nil

	case modalProxy:
		return m.handleProxyModalKey(msg)

	case modalTools:
		return m.handleToolsModalKey(msg)

	case modalMobile:
		return m.handleMobileModalKey(msg)

	case modalControlCenter:
		return m.handleControlCenterKey(msg)

	case modalHelp:
		return m.handleHelpModalKey(msg)

	case modalQuota:
		switch msg.String() {
		case "u", "q":
			m.modal = modalNone
			m.mode = modeNav
			return m, nil
		case "r":
			m.loading = true
			return m, func() tea.Msg {
				_, err := m.client.ProbeQuota("")
				return actionResultMsg{
					toast:       "已刷新额度数据",
					err:         err,
					reloadState: true,
				}
			}
		case "s":
			runID := m.currentRunID()
			if runID == "" {
				m.errMsg = "请先在左侧选中一个正在运行的 Agent 会话"
				return m, nil
			}
			return m, func() tea.Msg {
				_, err := m.client.ProbeQuota(runID)
				return actionResultMsg{
					toast:       "已向当前会话发送 /usage (/status) 查询，结果将自动同步",
					err:         err,
					closeModal:  true,
					enterTerm:   true,
					selectRunID: runID,
					reloadState: true,
				}
			}
		}
		return m, nil

	case modalPreview:
		switch msg.String() {
		case "q":
			m.closePreviewModal()
			return m, nil
		case "g", "home":
			m.previewViewport.GotoTop()
			return m, nil
		case "G", "end":
			m.previewViewport.GotoBottom()
			return m, nil
		case "c", "y":
			target := m.previewData.Path
			if m.previewData.StartLine > 0 {
				if m.previewData.EndLine > m.previewData.StartLine {
					target = fmt.Sprintf("%s:%d-%d", target, m.previewData.StartLine, m.previewData.EndLine)
				} else {
					target = fmt.Sprintf("%s:%d", target, m.previewData.StartLine)
				}
			}
			_ = copyToClipboard(target)
			m.toast = "已复制路径: " + target
			return m, nil
		case "o":
			if err := openLocalFileExternal(m.previewData.Path, m.previewData.StartLine); err != nil {
				m.errMsg = "外部打开失败: " + err.Error()
			} else {
				m.toast = "已在外部程序中打开 " + m.previewData.Path
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.previewViewport, cmd = m.previewViewport.Update(msg)
		return m, cmd

	case modalResume:
		total := m.resumeTotalItems()
		switch msg.String() {
		case "up", "k":
			if m.modalField == 0 && total > 0 {
				m.resumeCursor = (m.resumeCursor - 1 + total) % total
				m.syncResumeInputFocus()
				return m, nil
			}
		case "down", "j":
			if m.modalField == 0 && total > 0 {
				m.resumeCursor = (m.resumeCursor + 1) % total
				m.syncResumeInputFocus()
				return m, nil
			}
		case "tab":
			if m.resumeActionAt(m.resumeCursor) == "id" {
				m.modalField = (m.modalField + 1) % 2
				m.syncResumeInputFocus()
			}
			return m, nil
		case "b":
			if m.modalField == 0 {
				var targetSID string
				if m.resumeCursor >= 0 && m.resumeCursor < len(m.resumeSessions) {
					targetSID = m.resumeSessions[m.resumeCursor].ID
				} else if m.resumeActionAt(m.resumeCursor) == "id" {
					targetSID = strings.TrimSpace(m.sessionInput.Value())
				}
				if targetSID == "" {
					m.errMsg = "请先选中一个历史 Session 或输入 Session ID"
					return m, nil
				}
				runID := m.resumeRunID
				short := targetSID
				if len(short) > 8 {
					short = short[:8]
				}
				m.loading = true
				return m, func() tea.Msg {
					_, err := m.client.BindRunSession(runID, targetSID)
					return actionResultMsg{
						toast:       "已绑定 Session: " + short,
						err:         err,
						closeModal:  true,
						reloadState: true,
					}
				}
			}
		case "enter":
			runID := m.resumeRunID
			if m.resumeCursor >= 0 && m.resumeCursor < len(m.resumeSessions) {
				chosen := m.resumeSessions[m.resumeCursor]
				short := chosen.ID
				if len(short) > 8 {
					short = short[:8]
				}
				m.loading = true
				return m, func() tea.Msg {
					_, err := m.client.RestartRun(runID, protocol.RestartRunRequest{
						Mode:      "id",
						SessionID: chosen.ID,
					})
					return actionResultMsg{
						toast:         "已绑定并恢复 Session: " + short,
						err:           err,
						reattachRunID: runID,
						enterTerm:     true,
						closeModal:    true,
						reloadState:   true,
					}
				}
			}
			mode := m.resumeActionAt(m.resumeCursor)
			sid := strings.TrimSpace(m.sessionInput.Value())
			if mode == "id" && sid == "" {
				m.modalField = 1
				m.syncResumeInputFocus()
				m.errMsg = "请先粘贴或输入 Session ID"
				return m, nil
			}
			m.loading = true
			return m, func() tea.Msg {
				_, err := m.client.RestartRun(runID, protocol.RestartRunRequest{
					Mode:      mode,
					SessionID: sid,
				})
				return actionResultMsg{
					toast:         "已切换/重启会话",
					err:           err,
					reattachRunID: runID,
					enterTerm:     true,
					closeModal:    true,
					reloadState:   true,
				}
			}
		}
		if m.modalField == 1 {
			var cmd tea.Cmd
			m.sessionInput, cmd = m.sessionInput.Update(msg)
			return m, cmd
		}
		return m, nil
	}

	return m, nil
}

func (m Model) submitAddProject(dirPath string) (tea.Model, tea.Cmd) {
	m.loading = true
	return m, func() tea.Msg {
		proj, err := m.client.AddProject(protocol.AddProjectRequest{RepoPath: dirPath})
		kindHint := "普通目录"
		if proj.IsGitRepo {
			kindHint = "Git 仓库"
		} else if proj.IsMultiRepo {
			kindHint = fmt.Sprintf("多仓目录 · %d个子仓", proj.SubRepoCount)
		}
		return actionResultMsg{
			toast:       fmt.Sprintf("已添加工作目录 %s (%s)", proj.Name, kindHint),
			err:         err,
			closeModal:  true,
			reloadState: true,
		}
	}
}

func (m Model) updateModalNonKey(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.modal {
	case modalAddProject:
		if m.creatingDir {
			m.mkdirInput, cmd = m.mkdirInput.Update(msg)
		} else if m.manualPath {
			m.pathInput, cmd = m.pathInput.Update(msg)
		}
	case modalNewTask:
		if m.modalField == 0 {
			m.titleInput, cmd = m.titleInput.Update(msg)
		} else if m.modalField == 1 {
			m.promptInput, cmd = m.promptInput.Update(msg)
		} else if m.modalField == 5 {
			m.magpieInput, cmd = m.magpieInput.Update(msg)
		}
	case modalAddRuns:
		if m.modalField == 1 {
			m.promptInput, cmd = m.promptInput.Update(msg)
		} else if m.modalField == 3 {
			m.magpieInput, cmd = m.magpieInput.Update(msg)
		}
	case modalRenameTask:
		m.titleInput, cmd = m.titleInput.Update(msg)
	case modalAdopt:
		if m.modalField == 1 {
			m.commitInput, cmd = m.commitInput.Update(msg)
		}
	case modalPreview:
		m.previewViewport, cmd = m.previewViewport.Update(msg)
	case modalResume:
		if m.modalField == 1 {
			m.sessionInput, cmd = m.sessionInput.Update(msg)
		}
	case modalProxy:
		switch m.modalField {
		case 1:
			m.proxyHTTPInput, cmd = m.proxyHTTPInput.Update(msg)
		case 2:
			m.proxyHTTPSInput, cmd = m.proxyHTTPSInput.Update(msg)
		case 3:
			m.proxyNoProxyInput, cmd = m.proxyNoProxyInput.Update(msg)
		}
	case modalTools:
		if m.toolsEditingPath {
			m.toolsPathInput, cmd = m.toolsPathInput.Update(msg)
		}
	}
	return m, cmd
}

func (m *Model) syncResumeInputFocus() {
	const maxVis = 10
	if m.resumeCursor < len(m.resumeSessions) {
		if m.resumeCursor < m.resumeScroll {
			m.resumeScroll = m.resumeCursor
		}
		if m.resumeCursor >= m.resumeScroll+maxVis {
			m.resumeScroll = m.resumeCursor - maxVis + 1
		}
	}
	if m.resumeActionAt(m.resumeCursor) == "id" && m.modalField == 1 {
		m.sessionInput.Focus()
	} else {
		m.sessionInput.Blur()
		if m.resumeActionAt(m.resumeCursor) != "id" {
			m.modalField = 0
		}
	}
}

func (m *Model) syncModalFocus() {
	if m.modalField == 0 {
		m.titleInput.Focus()
	} else {
		m.titleInput.Blur()
	}
	if m.modalField == 1 {
		m.promptInput.Focus()
	} else {
		m.promptInput.Blur()
	}
	if m.modalField == 5 {
		m.magpieInput.Focus()
	} else {
		m.magpieInput.Blur()
	}
}

func (m Model) submitAddRuns() (tea.Model, tea.Cmd) {
	var selected []string
	for _, a := range m.snap.Agents {
		if m.agentChecked[a.Name] {
			selected = append(selected, a.Name)
		}
	}
	if len(selected) == 0 {
		m.errMsg = "请至少选择一个可用的 Agent"
		return m, nil
	}
	taskID := m.addRunsTaskID
	prompt := strings.TrimSpace(m.promptInput.Value())
	perm := m.permOptions[m.permIdx]
	magpieModel := strings.TrimSpace(m.magpieInput.Value())

	m.loading = true
	return m, func() tea.Msg {
		runs, err := m.client.AddTaskRuns(taskID, protocol.AddRunsRequest{
			Agents:      selected,
			Prompt:      prompt,
			PermMode:    perm,
			MagpieModel: magpieModel,
		})
		if err != nil {
			return actionResultMsg{err: err}
		}
		firstRun := ""
		if len(runs) > 0 {
			firstRun = runs[0].ID
		}
		return actionResultMsg{
			toast:         fmt.Sprintf("已向任务追加 %d 个 Agent (%s)", len(runs), strings.Join(selected, ", ")),
			selectRunID:   firstRun,
			reattachRunID: firstRun,
			enterTerm:     true,
			closeModal:    true,
			reloadState:   true,
		}
	}
}

func (m Model) submitNewTask() (tea.Model, tea.Cmd) {
	projID := m.currentProjectID()
	var selected []string
	for _, a := range m.snap.Agents {
		if m.agentChecked[a.Name] {
			selected = append(selected, a.Name)
		}
	}
	if len(selected) == 0 {
		m.errMsg = "请至少选择一个可用的 Agent"
		return m, nil
	}
	title := strings.TrimSpace(m.titleInput.Value())
	prompt := strings.TrimSpace(m.promptInput.Value())
	perm := m.permOptions[m.permIdx]
	workMode := m.workModeOpts[m.workModeIdx]
	magpieModel := strings.TrimSpace(m.magpieInput.Value())

	m.loading = true
	return m, func() tea.Msg {
		resp, err := m.client.CreateTask(protocol.CreateTaskRequest{
			ProjectID:   projID,
			Title:       title,
			Prompt:      prompt,
			Agents:      selected,
			WorkMode:    workMode,
			PermMode:    perm,
			MagpieModel: magpieModel,
		})
		if err != nil {
			return actionResultMsg{err: err}
		}
		firstRun := ""
		if len(resp.Runs) > 0 {
			firstRun = resp.Runs[0].ID
		}
		return actionResultMsg{
			toast:       fmt.Sprintf("已启动 %d 个 Agent (%s)", len(resp.Runs), strings.Join(selected, ", ")),
			selectRunID: firstRun,
			closeModal:  true,
			reloadState: true,
		}
	}
}

func (m *Model) rebuildTree() {
	prevRun := m.currentRunID()
	prevTask := m.currentTaskID()
	prevProj := m.currentProjectID()

	var nodes []treeNode
	for _, p := range m.snap.Projects {
		nodes = append(nodes, treeNode{
			kind:      nodeProject,
			projectID: p.ID,
			label:     p.Name,
		})
		if m.collapsedProjects[p.ID] {
			continue
		}
		for _, t := range m.snap.Tasks {
			if t.ProjectID != p.ID {
				continue
			}
			nodes = append(nodes, treeNode{
				kind:      nodeTask,
				projectID: p.ID,
				taskID:    t.ID,
				label:     t.Title,
			})
			if m.collapsedTasks[t.ID] {
				continue
			}
			for _, r := range m.snap.Runs {
				if r.TaskID != t.ID {
					continue
				}
				nodes = append(nodes, treeNode{
					kind:      nodeRun,
					projectID: p.ID,
					taskID:    t.ID,
					runID:     r.ID,
					label:     r.Agent,
				})
			}
		}
	}
	m.nodes = nodes

	if len(m.nodes) == 0 {
		m.sel = 0
		return
	}
	// Restore previous selection if possible.
	if prevRun != "" && m.selectByRunID(prevRun) {
		return
	}
	if prevTask != "" {
		for i, n := range m.nodes {
			if n.kind == nodeTask && n.taskID == prevTask {
				m.sel = i
				return
			}
		}
	}
	if prevProj != "" {
		for i, n := range m.nodes {
			if n.kind == nodeProject && n.projectID == prevProj {
				m.sel = i
				return
			}
		}
	}
	if m.sel >= len(m.nodes) {
		m.sel = len(m.nodes) - 1
	}
}

func (m *Model) selectByRunID(runID string) bool {
	// Ensure parent project & task are expanded so the target run is visible.
	if r, ok := m.findRun(runID); ok {
		if m.collapsedProjects[r.ProjectID] || m.collapsedTasks[r.TaskID] {
			delete(m.collapsedProjects, r.ProjectID)
			delete(m.collapsedTasks, r.TaskID)
			m.rebuildTree()
		}
	}
	for i, n := range m.nodes {
		if n.kind == nodeRun && n.runID == runID {
			m.sel = i
			return true
		}
	}
	return false
}

func (m *Model) toggleCurrentCollapse() bool {
	if len(m.nodes) == 0 || m.sel >= len(m.nodes) {
		return false
	}
	n := m.nodes[m.sel]
	switch n.kind {
	case nodeProject:
		if m.collapsedProjects[n.projectID] {
			delete(m.collapsedProjects, n.projectID)
		} else {
			m.collapsedProjects[n.projectID] = true
		}
		m.rebuildTree()
		return true
	case nodeTask:
		if m.collapsedTasks[n.taskID] {
			delete(m.collapsedTasks, n.taskID)
		} else {
			m.collapsedTasks[n.taskID] = true
		}
		m.rebuildTree()
		return true
	}
	return false
}

func (m *Model) collapseOrJumpParent() bool {
	if len(m.nodes) == 0 || m.sel >= len(m.nodes) {
		return false
	}
	n := m.nodes[m.sel]
	switch n.kind {
	case nodeProject:
		if !m.collapsedProjects[n.projectID] {
			m.collapsedProjects[n.projectID] = true
			m.rebuildTree()
			return true
		}
	case nodeTask:
		if !m.collapsedTasks[n.taskID] {
			m.collapsedTasks[n.taskID] = true
			m.rebuildTree()
			return true
		}
		// Already collapsed -> jump up to parent project.
		for i := m.sel - 1; i >= 0; i-- {
			if m.nodes[i].kind == nodeProject && m.nodes[i].projectID == n.projectID {
				m.sel = i
				return true
			}
		}
	case nodeRun:
		// Jump up to parent task.
		for i := m.sel - 1; i >= 0; i-- {
			if m.nodes[i].kind == nodeTask && m.nodes[i].taskID == n.taskID {
				m.sel = i
				return true
			}
		}
	}
	return false
}

func (m *Model) expandOrStepChild() bool {
	if len(m.nodes) == 0 || m.sel >= len(m.nodes) {
		return false
	}
	n := m.nodes[m.sel]
	switch n.kind {
	case nodeProject:
		if m.collapsedProjects[n.projectID] {
			delete(m.collapsedProjects, n.projectID)
			m.rebuildTree()
			return true
		}
		if m.sel+1 < len(m.nodes) && m.nodes[m.sel+1].projectID == n.projectID {
			m.sel++
			return true
		}
	case nodeTask:
		if m.collapsedTasks[n.taskID] {
			delete(m.collapsedTasks, n.taskID)
			m.rebuildTree()
			return true
		}
		if m.sel+1 < len(m.nodes) && m.nodes[m.sel+1].taskID == n.taskID {
			m.sel++
			return true
		}
	}
	return false
}

func (m *Model) quickSwitchTaskRun(idx int) tea.Cmd {
	taskID := m.currentTaskID()
	if taskID == "" {
		return nil
	}
	var taskRuns []int
	for i, n := range m.nodes {
		if n.kind == nodeRun && n.taskID == taskID {
			taskRuns = append(taskRuns, i)
		}
	}
	if idx < 0 || idx >= len(taskRuns) {
		return nil
	}
	m.sel = taskRuns[idx]
	return m.syncPTYAttachment()
}

func (m *Model) jumpToNextWaiting() tea.Cmd {
	var waitingRunIDs []string
	for _, r := range m.snap.Runs {
		if r.Status == protocol.StatusWaitingInput {
			waitingRunIDs = append(waitingRunIDs, r.ID)
		}
	}
	if len(waitingRunIDs) == 0 {
		m.toast = "当前没有等待确认的任务"
		return nil
	}
	curRunID := m.currentRunID()
	targetRunID := waitingRunIDs[0]
	for i, id := range waitingRunIDs {
		if id == curRunID && i+1 < len(waitingRunIDs) {
			targetRunID = waitingRunIDs[i+1]
			break
		}
	}
	m.selectByRunID(targetRunID)
	m.mode = modeTerm
	m.resizeViewportAndPTY()
	return tea.Batch(m.syncPTYAttachment(), m.ackIfWaitingCmd(targetRunID))
}

func (m *Model) autoResumeRunCmd(runID string) tea.Cmd {
	m.loading = true
	m.errMsg = ""
	m.ptyScreen = mutedStyle.Render("⏳ 正在自动恢复上次会话...")
	return func() tea.Msg {
		_, err := m.client.RestartRun(runID, protocol.RestartRunRequest{
			Mode: "auto",
		})
		if err != nil {
			return actionResultMsg{err: err}
		}
		return actionResultMsg{
			toast:         "已自动恢复上次会话",
			reloadState:   true,
			reattachRunID: runID,
			enterTerm:     true,
		}
	}
}

func (m *Model) ackIfWaitingCmd(runID string) tea.Cmd {
	r, ok := m.findRun(runID)
	if !ok || r.Status != protocol.StatusWaitingInput {
		return nil
	}
	return func() tea.Msg {
		_ = m.client.AckRun(runID)
		return nil
	}
}

func (m *Model) currentWorkDir() string {
	if runID := m.currentRunID(); runID != "" {
		if r, ok := m.findRun(runID); ok && r.WorktreePath != "" {
			return r.WorktreePath
		}
	}
	if taskID := m.currentTaskID(); taskID != "" {
		if t, ok := m.findTask(taskID); ok && t.WorktreePath != "" {
			return t.WorktreePath
		}
	}
	if projID := m.currentProjectID(); projID != "" {
		if p, ok := m.findProject(projID); ok && p.RepoPath != "" {
			return p.RepoPath
		}
	}
	return m.cwd
}

func (m *Model) toggleShellCmd() tea.Cmd {
	if m.shellMode {
		m.shellMode = false
		m.ptyDead = false
		if strings.HasPrefix(m.shellID, "install-") {
			m.attachedRunID = ""
		}
		if m.currentRunID() == "" {
			m.mode = modeNav
			m.resizeViewportAndPTY()
		}
		return m.syncPTYAttachment()
	}
	dir := m.currentWorkDir()
	cols, rows := m.ptyDimensions()
	m.loading = true
	m.errMsg = ""
	return func() tea.Msg {
		resp, err := m.client.EnsureShellSized(dir, cols, rows)
		return shellReadyMsg{resp: resp, err: err}
	}
}

func (m *Model) syncPTYAttachment() tea.Cmd {
	if m.shellMode && m.shellID != "" {
		return m.attachPTYTarget(m.shellID)
	}
	return m.attachPTYTarget(m.currentRunID())
}

func (m *Model) attachPTYTarget(targetID string) tea.Cmd {
	if targetID == m.attachedRunID && (m.ptyConn != nil || m.ptyConnecting || m.ptyDead) {
		return nil
	}
	if m.ptyConn != nil {
		_ = m.ptyConn.Close()
		m.ptyConn = nil
	}
	m.ptyGen++
	m.ptyConnecting = false
	m.ptyDead = false
	m.attachedRunID = targetID
	m.ptyScreen = ""
	m.ptyDeferredScreen = ""
	m.ptyLinks = nil
	m.ptyDeferredLinks = nil
	m.ptyScrollOffset = 0
	m.ptyScrollMax = 0
	m.ptyCursor = nil
	m.termSel.clear()

	if targetID == "" {
		m.lastPTYRunID = ""
		m.lastPTYCols = 0
		m.lastPTYRows = 0
		return nil
	}
	m.ptyConnecting = true
	cols, rows := m.ptyDimensions()
	m.lastPTYRunID = targetID
	m.lastPTYCols = cols
	m.lastPTYRows = rows
	return m.connectPTYCmd(targetID, m.ptyGen, cols, rows)
}

func (m Model) linkAt(col, row int) string {
	for _, sp := range m.ptyLinks {
		if sp.Row == row && col >= sp.StartX && col <= sp.EndX && sp.URL != "" {
			return sp.URL
		}
	}
	return ""
}

func (m *Model) SetMouseDisabled(disabled bool) {
	m.mouseDisabled = disabled
}

func (m Model) toggleMouseMode() (tea.Model, tea.Cmd) {
	m.mouseDisabled = !m.mouseDisabled
	if m.mouseDisabled {
		m.toast = "已切换原生鼠标模式（支持 SSH 直接右键粘贴；按 , 控制中心或 F12 恢复）"
		return m, tea.DisableMouse
	}
	m.toast = "已恢复鼠标点击与滚轮（SSH 下请用 Ctrl+Shift+V / Cmd+V 或 Shift+右键粘贴）"
	return m, tea.EnableMouseCellMotion
}

func (m *Model) pasteFromLocalClipboard() {
	clip, err := readFromClipboard()
	if err == nil && clip != "" {
		_ = daemon.SendPTYPaste(m.ptyConn, clip)
		return
	}
	if isRemoteSSHSession() || err != nil {
		m.toast = "SSH 远程无法读本地剪贴板：请用 Ctrl+Shift+V / Cmd+V 或 Shift+右键粘贴（按 F12 切原生右键模式）"
	}
}

func (m *Model) copyCurrentSelectionOrScreen() {
	if m.ptyScreen == "" {
		m.errMsg = "当前没有可复制的终端内容"
		return
	}
	var text string
	if m.termSel.active {
		_, rows := m.ptyDimensions()
		screenLines := strings.Split(m.ptyScreen, "\n")
		if len(screenLines) > rows {
			screenLines = screenLines[:rows]
		}
		text = extractSelectedText(screenLines, m.termSel)
	}
	if text == "" {
		text = stripANSI(m.ptyScreen)
	}
	_ = copyToClipboard(text)
	m.copyNotice = fmt.Sprintf("✓ 已复制 %d 字", len([]rune(text)))
	m.copyNoticeAt = time.Now()
	m.toast = m.copyNotice
}

// sidebarWidth returns the responsive left-sidebar outer width so narrow / laptop
// terminals never overflow or squeeze the Agent terminal below usable width.
func (m Model) sidebarWidth() int {
	if m.fullscreen || m.width <= 0 {
		return 0
	}
	if m.width < 68 {
		// On ultra-narrow windows, auto-hide sidebar while typing in TERM mode (Ctrl+] brings it back).
		if m.mode == modeTerm {
			return 0
		}
		w := m.width / 3
		if w < 18 {
			w = 18
		}
		if w > 22 {
			w = 22
		}
		if m.width-w < 24 {
			return 0
		}
		return w
	}
	if m.width < 105 {
		return 26
	}
	return 34
}

func (m Model) layoutWidths() (sidebarW, mainW int) {
	sidebarW = m.sidebarWidth()
	availW := m.width
	// Reserve 1 column at the right edge of the terminal so:
	// 1. Bubble Tea always appends EraseLineRight (\x1b[K]) on every row.
	// 2. Host terminals (e.g. WezTerm with emoji / ambiguous-width glyphs) never hit
	//    pending-wrap on the last column and shift Bubble Tea's line-diff renderer.
	if availW >= 40 {
		availW--
	}
	mainW = max(16, availW-sidebarW)
	return sidebarW, mainW
}

func (m *Model) ptyDimensions() (int, int) {
	if m.width <= 0 || m.height <= 0 {
		return 80, 24
	}
	_, mainW := m.layoutWidths()
	// Footer height is locked to 1 row in ALL modes (NAV / TERM / DIFF / MODAL) so
	// clicking between sidebar and terminal or silent background refreshes never change
	// bodyH or trigger spurious SIGWINCH redraws.
	const footerH = 1
	// Right pane inner content width is mainW - 4 (2 border + 2 horizontal padding).
	cols := mainW - 4
	// 2 (border top+bottom) + 2 (pane header + divider) + 1 (locked footer).
	rows := m.height - 4 - footerH
	if cols < 20 {
		cols = 20
	}
	if rows < 6 {
		rows = 6
	}
	return cols, rows
}

func (m *Model) resizeViewportAndPTY() {
	cols, rows := m.ptyDimensions()
	m.diffViewport.Width = cols
	m.diffViewport.Height = max(8, rows-8)
	if m.ptyConn == nil {
		return
	}
	if m.lastPTYRunID == m.attachedRunID && m.lastPTYCols == cols && m.lastPTYRows == rows {
		return
	}
	m.lastPTYRunID = m.attachedRunID
	m.lastPTYCols = cols
	m.lastPTYRows = rows
	_ = daemon.SendPTYResize(m.ptyConn, cols, rows)
}

func (m *Model) refreshDiffViewport() {
	if m.diffData == nil || len(m.diffData.Runs) == 0 {
		m.diffViewport.SetContent(mutedStyle.Render("暂无变更"))
		return
	}
	r := m.diffData.Runs[m.diffRunIdx]
	if r.Error != "" {
		m.diffViewport.SetContent(errorStyle.Render("读取 Diff 失败: " + r.Error))
		return
	}
	if strings.TrimSpace(r.DiffText) == "" {
		m.diffViewport.SetContent(mutedStyle.Render("该 Agent 尚未产生代码改动"))
		return
	}
	var out strings.Builder
	for _, line := range strings.Split(r.DiffText, "\n") {
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "diff --git"):
			out.WriteString(headerTitleStyle.Render(line))
		case strings.HasPrefix(line, "@@"):
			out.WriteString(diffHunkStyle.Render(line))
		case strings.HasPrefix(line, "+"):
			out.WriteString(diffAddStyle.Render(line))
		case strings.HasPrefix(line, "-"):
			out.WriteString(diffDelStyle.Render(line))
		default:
			out.WriteString(line)
		}
		out.WriteByte('\n')
	}
	m.diffViewport.SetContent(out.String())
	m.diffViewport.GotoTop()
}

func (m *Model) cleanupConns() {
	if m.cursorSync != nil {
		m.cursorSync.Clear()
	}
	if m.ptyConn != nil {
		_ = m.ptyConn.Close()
		m.ptyConn = nil
	}
	if m.evConn != nil {
		_ = m.evConn.Close()
		m.evConn = nil
	}
}

func (m Model) currentProjectID() string {
	if len(m.nodes) > 0 && m.sel < len(m.nodes) {
		return m.nodes[m.sel].projectID
	}
	if len(m.snap.Projects) > 0 {
		return m.snap.Projects[0].ID
	}
	return ""
}

func (m Model) currentTaskID() string {
	if len(m.nodes) > 0 && m.sel < len(m.nodes) {
		return m.nodes[m.sel].taskID
	}
	return ""
}

func (m Model) currentRunID() string {
	if len(m.nodes) > 0 && m.sel < len(m.nodes) {
		return m.nodes[m.sel].runID
	}
	return ""
}

func (m Model) findProject(id string) (protocol.Project, bool) {
	for _, p := range m.snap.Projects {
		if p.ID == id {
			return p, true
		}
	}
	return protocol.Project{}, false
}

func (m Model) findTask(id string) (protocol.Task, bool) {
	for _, t := range m.snap.Tasks {
		if t.ID == id {
			return t, true
		}
	}
	return protocol.Task{}, false
}

func (m Model) findRun(id string) (protocol.Run, bool) {
	for _, r := range m.snap.Runs {
		if r.ID == id {
			return r, true
		}
	}
	return protocol.Run{}, false
}

func (m Model) taskTitle(id string) string {
	if t, ok := m.findTask(id); ok {
		return t.Title
	}
	return id
}

// --- View Rendering ---

func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "正在初始化 aio..."
	}

	m.syncHostCursor()

	// Footer is permanently locked to 1 row across all modes to eliminate vertical jumps
	// on click and during silent background refreshes.
	const footerH = 1
	bodyH := max(8, m.height-footerH)
	sidebarW, mainW := m.layoutWidths()
	footerW := max(20, sidebarW+mainW)
	footer := m.renderFooter(footerW)

	var body string
	if m.mode == modeModal {
		body = m.renderModalOverlay(footerW, bodyH)
	} else if sidebarW == 0 {
		body = m.renderMainPane(mainW, bodyH)
	} else {
		left := m.renderSidebar(sidebarW, bodyH)
		right := m.renderMainPane(mainW, bodyH)
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	}

	return lipgloss.JoinVertical(lipgloss.Left, body, footer)
}

func (m Model) syncHostCursor() {
	if m.cursorSync == nil {
		return
	}
	if m.mode == modeTerm && m.ptyCursor != nil && !m.ptyCursor.Hidden {
		sidebarW := m.sidebarWidth()
		// Right pane 0-based origin: X = sidebarW + 1(border) + 1(padding), Y = 1(border) + 1(header) + 1(divider).
		// Convert to 1-based ANSI CUP coordinates.
		col := sidebarW + 2 + m.ptyCursor.X + 1
		row := 3 + m.ptyCursor.Y + 1
		if col > m.width {
			col = m.width
		}
		if row > m.height {
			row = m.height
		}
		m.cursorSync.SetTarget(row, col)
		return
	}
	m.cursorSync.Clear()
}

func (m Model) quotaForAgent(agent string) (protocol.AgentQuota, bool) {
	for _, q := range m.snap.Quotas {
		if q.Agent == agent {
			return q, true
		}
	}
	return protocol.AgentQuota{}, false
}

func renderQuotaBar(remPct float64) string {
	const width = 6
	filled := int((remPct/100.0)*float64(width) + 0.5)
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	return strings.Repeat("━", filled) + strings.Repeat("─", width-filled)
}

func renderQuotaBarStyled(remPct float64, barColor lipgloss.Color) string {
	const width = 6
	filled := int((remPct/100.0)*float64(width) + 0.5)
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	fillStr := lipgloss.NewStyle().Foreground(barColor).Render(strings.Repeat("━", filled))
	emptyStr := dividerStyle.Render(strings.Repeat("─", width-filled))
	return fillStr + emptyStr
}

func compactQuotaSummary(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, " 剩 ", " ")
	s = strings.ReplaceAll(s, " · 正常", "")
	return s
}

func (m Model) visibleQuotas() []protocol.AgentQuota {
	if len(m.snap.Quotas) == 0 {
		return nil
	}
	allowed := make(map[string]bool)
	if len(m.snap.Runs) > 0 {
		for _, r := range m.snap.Runs {
			if r.Agent != "" {
				allowed[r.Agent] = true
			}
		}
	} else if len(m.snap.Agents) > 0 {
		for _, a := range m.snap.Agents {
			if a.Available {
				allowed[a.Name] = true
			}
		}
	}
	if len(allowed) == 0 {
		return m.snap.Quotas
	}
	out := make([]protocol.AgentQuota, 0, len(m.snap.Quotas))
	for _, q := range m.snap.Quotas {
		if allowed[q.Agent] {
			out = append(out, q)
		}
	}
	return out
}

func (m Model) renderQuotaPanel(innerW int) []string {
	quotas := m.visibleQuotas()
	if len(quotas) == 0 {
		return nil
	}
	title := joinLeftRight(
		brandIconStyle.Render("◈")+" "+headerTitleStyle.Render("实时额度"),
		mutedStyle.Render(", → 5"),
		innerW,
	)
	lines := []string{
		dividerStyle.Render(strings.Repeat("─", innerW)),
		title,
	}
	agColW := 6
	for _, q := range quotas {
		if l := len(q.Agent); l > agColW && l <= 8 {
			agColW = l
		}
	}
	for _, q := range quotas {
		var dot string
		var sumStyled string
		sumText := compactQuotaSummary(q.Summary)
		textW := max(6, innerW-agColW-5)
		barTextW := max(6, innerW-agColW-12)
		switch q.State {
		case "limited":
			dot = errorStyle.Render("✗")
			sumStyled = errorStyle.Render(truncate(sumText, textW))
		case "low":
			dot = warnStyle.Render("▲")
			if q.RemainingPct != nil && innerW >= 26 {
				bar := renderQuotaBarStyled(*q.RemainingPct, colorWarn)
				sumStyled = bar + " " + warnStyle.Render(truncate(sumText, barTextW))
			} else {
				sumStyled = warnStyle.Render(truncate(sumText, textW))
			}
		case "ok":
			dot = okStyle.Render("●")
			if q.RemainingPct != nil && innerW >= 26 {
				bar := renderQuotaBarStyled(*q.RemainingPct, colorTermFocus)
				sumStyled = bar + " " + softTextStyle.Render(truncate(sumText, barTextW))
			} else {
				sumStyled = softTextStyle.Render(truncate(sumText, textW))
			}
		default:
			dot = mutedStyle.Render("○")
			sumStyled = mutedStyle.Render(truncate(sumText, textW))
		}
		agLabel := lipgloss.NewStyle().Foreground(agentColor(q.Agent)).Render(fmt.Sprintf("%-*s", agColW, q.Agent))
		lines = append(lines, ansi.Truncate(fmt.Sprintf(" %s %s %s", dot, agLabel, sumStyled), innerW, "…"))
	}
	return lines
}

func (m Model) projectTaskSummary(projectID string) (taskCount int, hasRunning bool, hasWaiting bool) {
	for _, t := range m.snap.Tasks {
		if t.ProjectID == projectID {
			taskCount++
		}
	}
	for _, r := range m.snap.Runs {
		if r.ProjectID == projectID {
			if r.Status == protocol.StatusWaitingInput {
				hasWaiting = true
			} else if r.Status == protocol.StatusRunning || r.Status == protocol.StatusStarting {
				hasRunning = true
			}
		}
	}
	return
}

func (m Model) taskRunSummary(taskID string) (runCount int, hasRunning bool, hasWaiting bool) {
	for _, r := range m.snap.Runs {
		if r.TaskID == taskID {
			runCount++
			if r.Status == protocol.StatusWaitingInput {
				hasWaiting = true
			} else if r.Status == protocol.StatusRunning || r.Status == protocol.StatusStarting {
				hasRunning = true
			}
		}
	}
	return
}

func (m Model) hasMoreTasksInProject(idx int, projectID string) bool {
	for j := idx + 1; j < len(m.nodes); j++ {
		if m.nodes[j].projectID != projectID {
			return false
		}
		if m.nodes[j].kind == nodeTask {
			return true
		}
	}
	return false
}

func (m Model) hasMoreRunsInTask(idx int, taskID string) bool {
	return idx+1 < len(m.nodes) && m.nodes[idx+1].kind == nodeRun && m.nodes[idx+1].taskID == taskID
}

func (m Model) projectBadgeText(projectID string) string {
	p, ok := m.findProject(projectID)
	if !ok {
		return ""
	}
	switch {
	case p.IsMultiRepo:
		return "多仓"
	case p.IsGitRepo:
		return "Git"
	default:
		return "目录"
	}
}

func (m Model) renderSidebar(w, h int) string {
	innerW := max(16, w-4)
	innerH := max(6, h-2)

	titleLine := joinLeftRight(
		brandIconStyle.Render("◈")+" "+headerTitleStyle.Render("AIO")+" "+mutedStyle.Render(version.Short()),
		mutedStyle.Render(fmt.Sprintf("%d 项目", len(m.snap.Projects))),
		innerW,
	)
	headerLines := []string{
		titleLine,
		dividerStyle.Render(strings.Repeat("─", innerW)),
	}

	quotaLines := m.renderQuotaPanel(innerW)
	availTreeH := innerH - len(headerLines)
	if len(quotaLines) > 0 && innerH > len(quotaLines)+len(headerLines)+3 {
		availTreeH = innerH - len(headerLines) - len(quotaLines)
	} else {
		quotaLines = nil
	}

	var treeLines []string
	if len(m.nodes) == 0 {
		treeLines = append(treeLines, "")
		treeLines = append(treeLines, mutedStyle.Render("  暂无工作目录"))
		treeLines = append(treeLines, mutedStyle.Render("  按 p 选择本地目录"))
	} else {
		// Keep selected row visible within the scrollable tree viewport.
		scroll := m.sidebarScroll
		if m.sel < scroll {
			scroll = m.sel
		}
		if availTreeH > 0 && m.sel >= scroll+availTreeH {
			scroll = m.sel - availTreeH + 1
		}
		if scroll < 0 {
			scroll = 0
		}
		end := len(m.nodes)
		if availTreeH > 0 && scroll+availTreeH < end {
			end = scroll + availTreeH
		}

		for i := scroll; i < end; i++ {
			n := m.nodes[i]
			isSel := i == m.sel
			var line string
			switch n.kind {
			case nodeProject:
				collapsed := m.collapsedProjects[n.projectID]
				chev := "▾"
				rightPlain := m.projectBadgeText(n.projectID)
				rightStyled := treeGuideStyle.Render(rightPlain)
				if collapsed {
					chev = "▸"
					cnt, running, waiting := m.projectTaskSummary(n.projectID)
					if waiting {
						rightPlain = fmt.Sprintf("%d ▲", cnt)
						rightStyled = warnStyle.Render(rightPlain)
					} else if running {
						rightPlain = fmt.Sprintf("%d ●", cnt)
						rightStyled = lipgloss.NewStyle().Foreground(colorPrimary).Render(rightPlain)
					} else if cnt > 0 {
						rightPlain = fmt.Sprintf("%d 任务", cnt)
						rightStyled = mutedStyle.Render(rightPlain)
					}
				}
				nameMax := max(6, innerW-5-ansi.StringWidth(rightPlain))
				name := truncate(n.label, nameMax)
				if isSel {
					line = joinLeftRight("▎ "+chev+" "+name, rightPlain, innerW)
				} else {
					chevStyled := lipgloss.NewStyle().Foreground(colorPrimary).Render(chev)
					if collapsed {
						chevStyled = mutedStyle.Render(chev)
					}
					line = joinLeftRight("  "+chevStyled+" "+headerTitleStyle.Render(name), rightStyled, innerW)
				}

			case nodeTask:
				collapsed := m.collapsedTasks[n.taskID]
				branch := "└"
				if m.hasMoreTasksInProject(i, n.projectID) {
					branch = "├"
				}
				prefixPlain := branch + " "
				prefixStyled := treeGuideStyle.Render(branch) + " "
				if collapsed {
					prefixPlain = branch + " ▸ "
					prefixStyled = treeGuideStyle.Render(branch) + " " + mutedStyle.Render("▸") + " "
				} else if t, ok := m.findTask(n.taskID); ok && t.Mode == "compare" {
					prefixPlain = branch + " ⇄ "
					prefixStyled = treeGuideStyle.Render(branch) + " " + lipgloss.NewStyle().Foreground(colorPrimary).Render("⇄") + " "
				}

				rightPlain := ""
				rightStyled := ""
				if collapsed {
					cnt, running, waiting := m.taskRunSummary(n.taskID)
					if waiting {
						rightPlain = fmt.Sprintf("%d ▲", cnt)
						rightStyled = warnStyle.Render(rightPlain)
					} else if running {
						rightPlain = fmt.Sprintf("%d ●", cnt)
						rightStyled = lipgloss.NewStyle().Foreground(colorPrimary).Render(rightPlain)
					} else if cnt > 0 {
						rightPlain = fmt.Sprintf("%d", cnt)
						rightStyled = mutedStyle.Render(rightPlain)
					}
				}
				nameMax := max(6, innerW-4-ansi.StringWidth(prefixPlain)-ansi.StringWidth(rightPlain))
				name := truncate(n.label, nameMax)
				if isSel {
					line = joinLeftRight("▎   "+prefixPlain+name, rightPlain, innerW)
				} else {
					line = joinLeftRight("    "+prefixStyled+lipgloss.NewStyle().Foreground(colorText).Render(name), rightStyled, innerW)
				}

			case nodeRun:
				vert := " "
				if m.hasMoreTasksInProject(i, n.projectID) {
					vert = "│"
				}
				branch := "└"
				if m.hasMoreRunsInTask(i, n.taskID) {
					branch = "├"
				}
				st := protocol.StatusStopped
				if r, ok := m.findRun(n.runID); ok {
					st = r.Status
				}
				rightPlain := statusShortText(st)
				if isSel {
					sym := statusSymbol(st)
					left := fmt.Sprintf("▎   %s %s %s %s", vert, branch, sym, n.label)
					line = joinLeftRight(left, rightPlain, innerW)
				} else {
					guide := treeGuideStyle.Render(vert + " " + branch)
					left := fmt.Sprintf("    %s %s %s", guide, statusIcon(st), agentNameStyled(n.label))
					line = joinLeftRight(left, statusShortStyled(st), innerW)
				}
			}
			if isSel {
				rowStyle := selectedRowStyle
				if m.mode == modeTerm {
					rowStyle = selectedInactiveRowStyle
				}
				line = rowStyle.Width(innerW).Render(line)
			}
			treeLines = append(treeLines, line)
		}
	}

	lines := append(headerLines, treeLines...)
	if len(quotaLines) > 0 {
		maxTreeLines := innerH - len(quotaLines)
		if len(lines) > maxTreeLines {
			lines = lines[:maxTreeLines]
		}
		for len(lines) < maxTreeLines {
			lines = append(lines, "")
		}
		lines = append(lines, quotaLines...)
	}

	content := strings.Join(lines, "\n")
	style := sidebarStyle
	if m.mode == modeNav {
		style = sidebarFocusedStyle
	}
	return style.Width(w - 2).Height(h - 2).MaxWidth(w).MaxHeight(h).Render(content)
}

func (m Model) renderMainPane(w, h int) string {
	if m.mode == modeDiff && m.diffData != nil {
		return m.renderDiffPane(w, h)
	}

	if m.shellMode {
		return m.renderShellPane(w, h)
	}

	runID := m.currentRunID()
	if runID == "" {
		return m.renderOverviewPane(w, h)
	}

	run, ok := m.findRun(runID)
	if !ok {
		return m.renderOverviewPane(w, h)
	}
	proj, _ := m.findProject(run.ProjectID)
	task, _ := m.findTask(run.TaskID)

	innerW := max(16, w-4)
	modeBadge := badgeNavStyle.Render("NAV")
	boxStyle := mainPaneNavStyle
	if m.mode == modeTerm {
		modeBadge = badgeTermStyle.Render("TERM")
		boxStyle = mainPaneTermStyle
	}

	branchLabel := run.Branch
	if branchLabel == "" {
		branchLabel = "local"
	}

	leftHeader := fmt.Sprintf("%s  %s  %s %s %s  %s",
		modeBadge,
		agentNameStyled(run.Agent),
		softTextStyle.Render(proj.Name),
		treeGuideStyle.Render("/"),
		headerTitleStyle.Render(task.Title),
		statusLabel(run.Status),
	)

	var rightHeader string
	switch {
	case m.copyNotice != "" && time.Since(m.copyNoticeAt) < 4*time.Second:
		rightHeader = badgeTermStyle.Render(m.copyNotice)
	case m.ptyScrollOffset > 0:
		rightHeader = badgeWarnStyle.Render(fmt.Sprintf("↑ 回看 -%d 行", m.ptyScrollOffset))
	case m.mouseDisabled:
		rightHeader = badgeWarnStyle.Render("原生右键/复制 · , 控制中心恢复")
	case m.mode == modeTerm:
		rightHeader = mutedStyle.Render("双击Esc / " + m.escapeKeyLabel() + " 导航 · Ctrl+\\ 终端 · 滚轮回看")
	default:
		loc := branchLabel
		if run.SessionID != "" {
			sid := run.SessionID
			if len(sid) > 8 {
				sid = sid[:8]
			}
			if loc == "local" {
				loc = "sid:" + sid
			} else {
				loc = loc + " · " + sid
			}
		}
		rightHeader = mutedStyle.Render(loc + " · Enter 进终端")
	}

	header := joinLeftRight(leftHeader, rightHeader, innerW)
	if ansi.StringWidth(header) > innerW {
		header = ansi.Truncate(header, innerW, "…")
	}

	screen := m.ptyScreen
	if screen == "" {
		screen = mutedStyle.Render("正在连接终端输出...")
	} else {
		screen = convertPTYScreenForProfile(screen, m.colorProf)
		// Strictly clamp PTY screen both vertically and horizontally so shrinking the
		// terminal window never causes Lipgloss to re-wrap lines and break the layout.
		screenLines := clampScreenLines(screen, innerW, max(1, h-4))
		if m.termSel.active {
			screenLines = applySelectionHighlight(screenLines, m.termSel)
		}
		screen = strings.Join(screenLines, "\n")
	}

	content := header + "\n" + dividerStyle.Render(strings.Repeat("─", innerW)) + "\n" + screen
	return boxStyle.Width(w - 2).Height(h - 2).MaxWidth(w).MaxHeight(h).Render(content)
}

func (m Model) renderShellPane(w, h int) string {
	innerW := max(16, w-4)
	isInstall := strings.HasPrefix(m.shellID, "install-")
	modeBadge := badgeNavStyle.Render("NAV")
	boxStyle := mainPaneNavStyle
	switch {
	case isInstall && m.ptyDead:
		modeBadge = badgeNavStyle.Render("INSTALL 结束")
	case isInstall:
		modeBadge = badgeShellStyle.Render("INSTALL")
		if m.mode == modeTerm {
			boxStyle = mainPaneShellStyle
		}
	case m.mode == modeTerm:
		modeBadge = badgeShellStyle.Render("SHELL")
		boxStyle = mainPaneShellStyle
	}
	shName := m.shellBin
	if shName == "" {
		shName = "shell"
	}
	dirDisp := m.shellDir
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(dirDisp, home) {
		dirDisp = "~" + strings.TrimPrefix(dirDisp, home)
	}
	leftHeader := fmt.Sprintf("%s  %s  %s",
		modeBadge,
		lipgloss.NewStyle().Foreground(colorAgentCursor).Bold(true).Render("$_ "+shName),
		headerTitleStyle.Render(dirDisp),
	)

	var rightHeader string
	switch {
	case m.copyNotice != "" && time.Since(m.copyNoticeAt) < 4*time.Second:
		rightHeader = badgeTermStyle.Render(m.copyNotice)
	case m.ptyScrollOffset > 0:
		rightHeader = badgeWarnStyle.Render(fmt.Sprintf("↑ 回看 -%d 行", m.ptyScrollOffset))
	case isInstall && m.ptyDead:
		rightHeader = softTextStyle.Render("Esc/Enter 返回 · , 控制中心")
	case isInstall:
		rightHeader = mutedStyle.Render("实时输出中 · " + m.escapeKeyLabel() + " 导航")
	case m.mode == modeTerm:
		rightHeader = mutedStyle.Render("Ctrl+\\ 切回Agent · 双击Esc / " + m.escapeKeyLabel() + " 导航")
	default:
		rightHeader = mutedStyle.Render("Enter 进终端 · t 切回Agent")
	}

	header := joinLeftRight(leftHeader, rightHeader, innerW)
	if ansi.StringWidth(header) > innerW {
		header = ansi.Truncate(header, innerW, "…")
	}

	screen := m.ptyScreen
	if isInstall && m.ptyDead {
		maxScreenH := max(1, h-6)
		if strings.TrimSpace(ansi.Strip(screen)) == "" {
			screen = mutedStyle.Render("安装进程已退出（无终端输出）")
		} else {
			screenLines := clampTailScreenLines(screen, innerW, maxScreenH)
			if m.termSel.active {
				screenLines = applySelectionHighlight(screenLines, m.termSel)
			}
			screen = strings.Join(screenLines, "\n")
		}
		actionBar := badgeNavStyle.Render("安装已结束") + "  " +
			softTextStyle.Render("按 ") + keyHintStyle.Render("Esc / Enter / t") + softTextStyle.Render(" 返回工作台  ·  按 ") +
			keyHintStyle.Render(",") + softTextStyle.Render(" 打开控制中心查看状态或调整代理")
		if ansi.StringWidth(actionBar) > innerW {
			actionBar = ansi.Truncate(actionBar, innerW, "…")
		}
		content := header + "\n" +
			dividerStyle.Render(strings.Repeat("─", innerW)) + "\n" +
			screen + "\n" +
			dividerStyle.Render(strings.Repeat("─", innerW)) + "\n" +
			actionBar
		return boxStyle.Width(w - 2).Height(h - 2).MaxWidth(w).MaxHeight(h).Render(content)
	}

	if screen == "" {
		if isInstall {
			screen = mutedStyle.Render("正在启动安装进程...")
		} else {
			screen = mutedStyle.Render("正在打开交互终端...")
		}
	} else {
		screen = convertPTYScreenForProfile(screen, m.colorProf)
		screenLines := clampScreenLines(screen, innerW, max(1, h-4))
		if m.termSel.active {
			screenLines = applySelectionHighlight(screenLines, m.termSel)
		}
		screen = strings.Join(screenLines, "\n")
	}

	content := header + "\n" + dividerStyle.Render(strings.Repeat("─", innerW)) + "\n" + screen
	return boxStyle.Width(w - 2).Height(h - 2).MaxWidth(w).MaxHeight(h).Render(content)
}

// clampTailScreenLines trims trailing visually-empty terminal rows and keeps the
// last maxH lines so completed installer logs always show their final exit status.
func clampTailScreenLines(screen string, maxW, maxH int) []string {
	lines := strings.Split(screen, "\n")
	for len(lines) > 0 && strings.TrimSpace(ansi.Strip(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > maxH {
		lines = lines[len(lines)-maxH:]
	}
	if maxW > 0 {
		for i, l := range lines {
			if strings.ContainsRune(l, '\r') {
				l = strings.ReplaceAll(l, "\r", "")
			}
			if ansi.StringWidth(l) > maxW {
				l = ansi.Truncate(l, maxW, "")
			}
			lines[i] = l
		}
	}
	return lines
}

func clampScreenLines(screen string, maxW, maxH int) []string {
	lines := strings.Split(screen, "\n")
	if len(lines) > maxH {
		lines = lines[:maxH]
	}
	if maxW > 0 {
		for i, l := range lines {
			if strings.ContainsRune(l, '\r') {
				l = strings.ReplaceAll(l, "\r", "")
			}
			if ansi.StringWidth(l) > maxW {
				l = ansi.Truncate(l, maxW, "")
			}
			lines[i] = l
		}
	}
	return lines
}

// renderOverviewPane is implemented in overview.go.

func (m Model) renderDiffPane(w, h int) string {
	d := m.diffData
	var b strings.Builder
	baseHint := "工作区实时 Diff（自动聚合子 Git 仓库）"
	if d.BaseCommit != "" {
		baseHint = "Base: " + d.BaseBranch + "@" + shortSHA(d.BaseCommit)
	}
	b.WriteString(badgeNavStyle.Render("DIFF 视图") + "  " +
		headerTitleStyle.Render(m.taskTitle(d.TaskID)) + "  " +
		mutedStyle.Render(baseHint) + "\n\n")

	// Horizontal summary table for all competing runs.
	b.WriteString(fmt.Sprintf("  %-10s %-10s %-8s %-14s %-8s\n", "Agent", "Status", "Files", "Changes", "Time"))
	b.WriteString("  " + mutedStyle.Render(strings.Repeat("─", min(62, max(20, w-8)))) + "\n")
	for i, r := range d.Runs {
		prefix := "  "
		if i == m.diffRunIdx {
			prefix = "▶ "
		}
		chg := fmt.Sprintf("+%d/-%d", r.Insertions, r.Deletions)
		row := fmt.Sprintf("%s%-10s %-10s %-8d %-14s %ds",
			prefix, r.Agent, string(r.Status), r.FilesChanged, chg, r.ElapsedSec)
		if i == m.diffRunIdx {
			row = selectedRowStyle.Render(row)
		}
		b.WriteString(row + "\n")
	}

	b.WriteString("\n" + mutedStyle.Render("←/→ 切换 Agent  ↑/↓ 滚动 Diff  r 刷新  a 采纳当前胜出者(Adopt)  Esc 返回") + "\n")
	b.WriteString(mutedStyle.Render(strings.Repeat("─", max(10, w-4))) + "\n")
	b.WriteString(m.diffViewport.View())

	return mainPaneNavStyle.Width(w - 2).Height(h - 2).MaxWidth(w).MaxHeight(h).Render(b.String())
}

func (m Model) renderModalOverlay(w, h int) string {
	var body string
	switch m.modal {
	case modalAddProject:
		if m.creatingDir {
			gitMark := "[ ]"
			if m.mkdirInitGit {
				gitMark = "[x]"
			}
			body = headerTitleStyle.Render("新建文件夹") + "\n\n" +
				mutedStyle.Render("所在目录：") + softTextStyle.Render(m.picker.currentDir) + "\n\n" +
				"文件夹名称（支持多级路径）：\n  " + m.mkdirInput.View() + "\n\n" +
				fmt.Sprintf("  %s 同时初始化为 Git 仓库 (git init)  %s\n\n", keyHintStyle.Render(gitMark), mutedStyle.Render("Tab 切换")) +
				mutedStyle.Render("Enter 确认创建并定位  ·  Esc 返回目录选择")
		} else if m.manualPath {
			body = headerTitleStyle.Render("添加本地工作目录（手动输入）") + "\n\n" +
				"本地目录路径：\n" + m.pathInput.View() + "\n\n" +
				mutedStyle.Render("Enter 确认添加  ·  Tab 返回目录选择  ·  Esc 取消")
		} else {
			body = headerTitleStyle.Render("选择本地工作目录") + "\n\n" +
				m.picker.render() + "\n" +
				mutedStyle.Render("↑/↓ 选择  ·  →/Tab 进子目录  ·  ←/Backspace 返回上级  ·  ~ 主目录  ·  . 隐藏目录") + "\n" +
				mutedStyle.Render("Enter 添加选中目录  ·  N 新建文件夹  ·  / 手动输入路径  ·  Esc 取消")
		}

	case modalNewTask:
		selectedCount := 0
		for _, checked := range m.agentChecked {
			if checked {
				selectedCount++
			}
		}
		kindTitle := "新建任务（支持多选 Agent 并行对比）"
		if selectedCount >= 2 {
			kindTitle = fmt.Sprintf("⚔ 新建任务（已选 %d 个 Agent 并行对比）", selectedCount)
		}
		var agentsBuf strings.Builder
		for i, a := range m.snap.Agents {
			isCur := m.modalField == 2 && i == m.agentCursor
			cursor := "  "
			if isCur {
				cursor = "▶ "
			}
			check := "[ ]"
			if m.agentChecked[a.Name] {
				check = "[x]"
			}
			avail := ""
			if !a.Available {
				avail = " (未安装)"
			} else if q, ok := m.quotaForAgent(a.Name); ok && q.Summary != "" {
				avail = "  [" + q.Summary + "]"
			}
			if avail != "" && !isCur {
				if q, ok := m.quotaForAgent(a.Name); ok && a.Available {
					switch q.State {
					case "limited":
						avail = errorStyle.Render(avail)
					case "low":
						avail = warnStyle.Render(avail)
					default:
						avail = mutedStyle.Render(avail)
					}
				} else {
					avail = mutedStyle.Render(avail)
				}
			}
			row := fmt.Sprintf("%s%s %-13s%s", cursor, check, a.Label, avail)
			if isCur {
				row = selectedRowStyle.Width(50).Render(row)
			}
			agentsBuf.WriteString(row + "\n")
		}
		var workModeBuf strings.Builder
		proj, _ := m.findProject(m.currentProjectID())
		wmLabels := map[string]string{
			"shared":   "共享Worktree(接力)",
			"local":    "直连原目录",
			"isolated": "独立Worktree(对比)",
		}
		for i, wm := range m.workModeOpts {
			label := wmLabels[wm]
			if i == m.workModeIdx {
				label = badgeNavStyle.Render(label)
			} else {
				label = mutedStyle.Render(" " + label + " ")
			}
			workModeBuf.WriteString(label + " ")
		}
		if proj.IsMultiRepo {
			workModeBuf.WriteString(okStyle.Render(fmt.Sprintf("(多仓组合 · %d个子仓)", proj.SubRepoCount)))
		} else if !proj.SupportsWorktree() {
			workModeBuf.WriteString(mutedStyle.Render("(纯普通目录)"))
		}

		var permBuf strings.Builder
		for i, pm := range m.permOptions {
			item := string(pm)
			if i == m.permIdx {
				item = badgeNavStyle.Render(item)
			} else {
				item = mutedStyle.Render(" " + item + " ")
			}
			permBuf.WriteString(item + " ")
		}
		focusMark := func(f int, s string) string {
			if m.modalField == f {
				return lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render("▸ " + s)
			}
			return "  " + s
		}
		magpieSection := ""
		if m.snap.Magpie.Available {
			magpieSection = focusMark(5, "Magpie 模型路由（可选，支持 Claude/Codex 跨厂路由）：") + "\n  " + m.magpieInput.View() + "\n\n"
		}
		body = headerTitleStyle.Render(kindTitle) + "\n\n" +
			focusMark(0, "任务名称（可留空自动生成）：") + "\n  " + m.titleInput.View() + "\n\n" +
			focusMark(1, "自动派发 Prompt：") + "\n  " + m.promptInput.View() + "\n\n" +
			focusMark(2, "选择 Agent（空格勾选 1 个为单任务，多选自动赛跑对比 · 按 , 可安装）：") + "\n" + agentsBuf.String() + "\n" +
			focusMark(3, "工作区模式（←/→ 切换）：") + "\n  " + workModeBuf.String() + "\n\n" +
			focusMark(4, "权限模式（←/→ 切换）：") + "\n  " + permBuf.String() + "\n\n" +
			magpieSection +
			mutedStyle.Render("Tab 切换字段  ·  Space 勾选  ·  Enter 立即启动  ·  Esc 取消")

	case modalAddRuns:
		existing := make(map[string]bool)
		for _, r := range m.snap.Runs {
			if r.TaskID == m.addRunsTaskID {
				existing[r.Agent] = true
			}
		}
		var agentsBuf strings.Builder
		for i, a := range m.snap.Agents {
			isCur := m.modalField == 0 && i == m.agentCursor
			cursor := "  "
			if isCur {
				cursor = "▶ "
			}
			check := "[ ]"
			if m.agentChecked[a.Name] {
				check = "[x]"
			}
			tag := ""
			if !a.Available {
				tag = " (未安装)"
			} else {
				if q, ok := m.quotaForAgent(a.Name); ok && q.Summary != "" {
					tag = "  [" + q.Summary + "]"
				}
				if existing[a.Name] {
					tag += " (已有)"
				}
			}
			if tag != "" && !isCur {
				if q, ok := m.quotaForAgent(a.Name); ok && a.Available {
					switch q.State {
					case "limited":
						tag = errorStyle.Render(tag)
					case "low":
						tag = warnStyle.Render(tag)
					default:
						tag = mutedStyle.Render(tag)
					}
				} else {
					tag = mutedStyle.Render(tag)
				}
			}
			row := fmt.Sprintf("%s%s %-13s%s", cursor, check, a.Label, tag)
			if isCur {
				row = selectedRowStyle.Width(52).Render(row)
			}
			agentsBuf.WriteString(row + "\n")
		}
		var permBuf strings.Builder
		for i, pm := range m.permOptions {
			item := string(pm)
			if i == m.permIdx {
				item = badgeNavStyle.Render(item)
			} else {
				item = mutedStyle.Render(" " + item + " ")
			}
			permBuf.WriteString(item + " ")
		}
		focusMark := func(f int, s string) string {
			if m.modalField == f {
				return lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render("▸ " + s)
			}
			return "  " + s
		}
		modeHint := ""
		if t, ok := m.findTask(m.addRunsTaskID); ok {
			switch t.WorkMode {
			case "shared":
				modeHint = okStyle.Render("复用共享 Worktree ("+t.Branch+")，无缝接力继续改同一份代码") + "\n\n"
			case "isolated", "worktree":
				modeHint = mutedStyle.Render("将为新 Agent 拉出独立对比 Worktree（基于同一 BaseCommit）") + "\n\n"
			default:
				modeHint = mutedStyle.Render("直连原工作目录运行") + "\n\n"
			}
		}
		magpieAddSection := ""
		if m.snap.Magpie.Available {
			magpieAddSection = focusMark(3, "Magpie 模型路由（可选，支持 Claude/Codex 跨厂路由）：") + "\n  " + m.magpieInput.View() + "\n\n"
		}
		body = headerTitleStyle.Render("向任务追加 Agent :: "+m.taskTitle(m.addRunsTaskID)) + "\n\n" +
			modeHint +
			focusMark(0, "选择要追加的 Agent（空格勾选/取消）：") + "\n" + agentsBuf.String() + "\n" +
			focusMark(1, "可选初始 Prompt（留空直接进交互终端）：") + "\n  " + m.promptInput.View() + "\n\n" +
			focusMark(2, "权限模式（←/→ 切换）：") + "\n  " + permBuf.String() + "\n\n" +
			magpieAddSection +
			mutedStyle.Render("Tab 切换字段  ·  Space 勾选  ·  Enter 立即追加并进入终端  ·  Esc 取消")

	case modalAdopt:
		winner, _ := m.findRun(m.adoptRunID)
		task, _ := m.findTask(m.adoptTaskID)
		var stratBuf strings.Builder
		labels := map[protocol.MergeStrategy]string{
			protocol.MergeSquash: "Squash and merge（推荐 — 合并为一个干净提交）",
			protocol.MergeCommit: "Create a merge commit（保留分支合并提交）",
			protocol.MergeRebase: "Rebase and merge（变基后快进合并）",
		}
		for i, st := range m.mergeOptions {
			mark := "( )"
			if i == m.mergeIdx {
				mark = "(●)"
			}
			line := fmt.Sprintf("  %s %s", mark, labels[st])
			if i == m.mergeIdx && m.modalField == 0 {
				line = selectedRowStyle.Render(line)
			}
			stratBuf.WriteString(line + "\n")
		}
		body = headerTitleStyle.Render("采纳胜出方案（Adopt Winner）") + "\n\n" +
			fmt.Sprintf("胜出 Agent：%s  (%s → %s)\n\n", okStyle.Render(winner.Agent), winner.Branch, task.BaseBranch) +
			"GitHub 合并方式（↑/↓ 选择）：\n" + stratBuf.String() + "\n" +
			"Commit Message（Tab 切换编辑，可留空）：\n  " + m.commitInput.View() + "\n\n" +
			warnStyle.Render("⚠ 合并成功后将自动删除该任务下所有其他 Agent 的 Worktree 和临时分支。") + "\n\n" +
			mutedStyle.Render("Enter 确认合并并清理  ·  Esc 取消")

	case modalRenameTask:
		body = headerTitleStyle.Render("重命名任务") + "\n\n" +
			"新任务名称：\n  " + m.titleInput.View() + "\n\n" +
			mutedStyle.Render("Enter 确认修改  ·  Esc 取消")

	case modalConfirmDelete:
		sessMark := "[ ]"
		if m.deleteCleanSession {
			sessMark = "[x]"
		}
		body = errorStyle.Render("确认清理删除") + "\n\n" +
			"确定要删除 " + headerTitleStyle.Render(m.deleteLabel) + " 吗？\n\n" +
			mutedStyle.Render("• 将强制终止对应 Agent 进程树（含子命令/MCP）并释放内存 PTY\n") +
			fmt.Sprintf("  %s 同时彻底删除磁盘上对应的本地 CLI 历史 Session  %s\n\n", keyHintStyle.Render(sessMark), mutedStyle.Render("Space 切换")) +
			mutedStyle.Render("y / Enter 确认删除  ·  n / Esc 取消")

	case modalProxy:
		body = m.renderProxyModalBody()

	case modalTools:
		body = m.renderToolsModalBody()

	case modalMobile:
		body = m.renderMobileModalBody()

	case modalControlCenter:
		body = m.renderControlCenterBody()

	case modalHelp:
		body = m.renderHelpModalBody()

	case modalQuota:
		var qBuf strings.Builder
		quotas := m.visibleQuotas()
		if len(quotas) == 0 {
			qBuf.WriteString(mutedStyle.Render("  当前暂无已开启或已安装的 Agent 额度记录\n\n"))
		}
		for _, q := range quotas {
			icon := mutedStyle.Render("○")
			sum := mutedStyle.Render(q.Summary)
			switch q.State {
			case "ok":
				icon = okStyle.Render("●")
				if q.RemainingPct != nil {
					sum = okStyle.Render(renderQuotaBar(*q.RemainingPct) + " " + q.Summary)
				} else {
					sum = okStyle.Render(q.Summary)
				}
			case "low":
				icon = warnStyle.Render("▲")
				if q.RemainingPct != nil {
					sum = warnStyle.Render(renderQuotaBar(*q.RemainingPct) + " " + q.Summary)
				} else {
					sum = warnStyle.Render(q.Summary)
				}
			case "limited":
				icon = errorStyle.Render("✗")
				sum = errorStyle.Render(q.Summary)
			}
			upd := ""
			if !q.UpdatedAt.IsZero() && q.State != "unknown" {
				upd = mutedStyle.Render("  (" + q.UpdatedAt.Format("15:04:05") + ")")
			}
			qBuf.WriteString(fmt.Sprintf("%s %-8s %s%s\n", icon, headerTitleStyle.Render(q.Agent), sum, upd))
			if q.Detail != "" {
				for _, dl := range strings.Split(q.Detail, "\n") {
					dl = strings.TrimSpace(dl)
					if dl != "" {
						qBuf.WriteString("    " + mutedStyle.Render(truncate(dl, 62)) + "\n")
					}
				}
			}
			qBuf.WriteByte('\n')
		}
		body = headerTitleStyle.Render("Agent 实时额度详情 (/usage · /status)") + "\n\n" +
			qBuf.String() +
			mutedStyle.Render("说明：自动按当前已开启的 Agent 动态展示（无任务时显示本机已安装 Agent）") + "\n" +
			mutedStyle.Render("r:立即后台静默刷新  ·  s:向当前选中会话发/usage  ·  Esc/u:关闭")

	case modalResume:
		run, _ := m.findRun(m.resumeRunID)
		proj, _ := m.findProject(run.ProjectID)
		const listW = 70

		boundInfo := mutedStyle.Render("当前绑定 Session：尚未绑定（发送消息或下方选择即可绑定）")
		if run.SessionID != "" {
			boundInfo = softTextStyle.Render("当前绑定 Session：") + keyHintStyle.Render(run.SessionID)
		}

		var listBuf strings.Builder
		if m.resumeLoading {
			listBuf.WriteString(mutedStyle.Render("  ⏳ 正在扫描本地历史 Session...") + "\n")
		} else if len(m.resumeSessions) == 0 {
			listBuf.WriteString(mutedStyle.Render("  （未扫描到该 Agent 的本地历史 Session，可使用下方选项）") + "\n")
		} else {
			maxVis := 10
			scroll := m.resumeScroll
			if m.resumeCursor < len(m.resumeSessions) {
				if m.resumeCursor < scroll {
					scroll = m.resumeCursor
				}
				if m.resumeCursor >= scroll+maxVis {
					scroll = m.resumeCursor - maxVis + 1
				}
			}
			end := len(m.resumeSessions)
			if scroll+maxVis < end {
				end = scroll + maxVis
			}
			if scroll > 0 {
				listBuf.WriteString(mutedStyle.Render(fmt.Sprintf("  ↑ 还有 %d 条更近的历史...", scroll)) + "\n")
			}
			for i := scroll; i < end; i++ {
				s := m.resumeSessions[i]
				isCur := i == m.resumeCursor && m.modalField == 0
				star := " "
				if run.SessionID != "" && s.ID == run.SessionID {
					star = "★"
				}
				scope := "[其他]"
				if s.SameDir {
					scope = "[本目录]"
				}
				shortID := s.ID
				if len(shortID) > 8 {
					shortID = shortID[:8]
				}
				relT := formatSessionTime(s.UpdatedAt)
				rightPlain := shortID
				if relT != "" {
					rightPlain = shortID + "  " + relT
				}
				titleMax := max(12, listW-4-ansi.StringWidth(scope)-ansi.StringWidth(rightPlain)-3)
				title := truncate(s.Title, titleMax)

				if isCur {
					left := fmt.Sprintf("▎ %s %s %s", star, scope, title)
					row := joinLeftRight(left, rightPlain, listW)
					listBuf.WriteString(selectedRowStyle.Width(listW).Render(row) + "\n")
				} else {
					starS := " "
					if star == "★" {
						starS = okStyle.Render("★")
					}
					scopeS := mutedStyle.Render(scope)
					if s.SameDir {
						scopeS = lipgloss.NewStyle().Foreground(colorPrimary).Render(scope)
					}
					left := fmt.Sprintf("  %s %s %s", starS, scopeS, lipgloss.NewStyle().Foreground(colorText).Render(title))
					row := joinLeftRight(left, mutedStyle.Render(rightPlain), listW)
					listBuf.WriteString(row + "\n")
				}
			}
			if end < len(m.resumeSessions) {
				listBuf.WriteString(mutedStyle.Render(fmt.Sprintf("  ↓ 还有 %d 条更早的历史...", len(m.resumeSessions)-end)) + "\n")
			}
		}

		listBuf.WriteString(dividerStyle.Render(strings.Repeat("─", listW)) + "\n")
		actions := []struct {
			icon  string
			label string
		}{
			{"+", "开启全新空白会话（解绑并创建新 Session）"},
			{"#", "手动输入 / 粘贴 Session ID 绑定"},
			{"▸", "打开 CLI 原生历史选择器（picker）"},
		}
		for idx, act := range actions {
			curIdx := len(m.resumeSessions) + idx
			isCur := curIdx == m.resumeCursor && m.modalField == 0
			if isCur {
				row := fmt.Sprintf("▎ %s %s", act.icon, act.label)
				listBuf.WriteString(selectedRowStyle.Width(listW).Render(row) + "\n")
			} else {
				listBuf.WriteString(fmt.Sprintf("  %s %s\n", keyHintStyle.Render(act.icon), softTextStyle.Render(act.label)))
			}
		}

		extra := ""
		if m.resumeActionAt(m.resumeCursor) == "id" {
			extra = "\nSession ID（Tab 切换编辑）：\n  " + m.sessionInput.View() + "\n"
		}
		titleHead := fmt.Sprintf("绑定 / 切换历史 Session (%s · %s)", run.Agent, proj.Name)
		body = headerTitleStyle.Render(titleHead) + "\n\n" +
			boundInfo + "\n\n" +
			listBuf.String() + extra + "\n" +
			mutedStyle.Render("↑/↓ 选择  ·  Enter 绑定并立即进入终端  ·  b 仅绑定不重启  ·  Esc 取消")

	case modalPreview:
		d := m.previewData
		vpW := max(40, m.previewViewport.Width)
		name := d.Path
		if idx := strings.LastIndex(name, "/"); idx >= 0 && idx+1 < len(name) {
			name = name[idx+1:]
		}
		locTag := ""
		if d.StartLine > 0 {
			if d.EndLine > d.StartLine {
				locTag = fmt.Sprintf("L%d-%d", d.StartLine, d.EndLine)
			} else {
				locTag = fmt.Sprintf("L%d", d.StartLine)
			}
		} else if d.IsDir {
			locTag = fmt.Sprintf("目录 · %d 项", d.TotalLines)
		} else {
			locTag = fmt.Sprintf("%d 行", d.TotalLines)
		}
		pct := int(m.previewViewport.ScrollPercent() * 100)
		topLine := joinLeftRight(
			brandIconStyle.Render("◈")+" "+headerTitleStyle.Render("文件预览")+"  "+keyHintStyle.Render(name)+"  "+mutedStyle.Render(locTag),
			mutedStyle.Render(fmt.Sprintf("%d%%", pct)),
			vpW,
		)
		pathLine := mutedStyle.Render(truncate(d.Path, vpW))
		body = topLine + "\n" +
			pathLine + "\n" +
			dividerStyle.Render(strings.Repeat("─", vpW)) + "\n" +
			m.previewViewport.View() + "\n" +
			dividerStyle.Render(strings.Repeat("─", vpW)) + "\n" +
			mutedStyle.Render("↑/↓/滚轮 翻页  ·  g/G 首尾  ·  c 复制路径  ·  o 外部编辑器打开  ·  Esc/q 关闭")
	}

	box := modalBoxStyle.Render(body)
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box)
}

func (m Model) renderFooter(w int) string {
	oneLine := func(s string) string {
		s = strings.ReplaceAll(s, "\r", " ")
		s = strings.ReplaceAll(s, "\n", " ")
		return lipgloss.NewStyle().MaxWidth(w).Render(ansi.Truncate(s, max(10, w), "…"))
	}
	// High-priority single-line alerts appear in any mode (including TERM) so nothing is missed.
	if m.errMsg != "" {
		return oneLine(errorStyle.Render("✗ " + m.errMsg))
	}
	if waiting := m.waitingRuns(); len(waiting) > 0 {
		first := waiting[0]
		proj, _ := m.findProject(first.ProjectID)
		task, _ := m.findTask(first.TaskID)
		return oneLine(
			badgeWarnStyle.Render(fmt.Sprintf("⚠ %d 等待确认", len(waiting))) + " " +
				warnStyle.Render(fmt.Sprintf("%s / %s / %s (Ctrl+] 后按 w 直达)", proj.Name, task.Title, first.Agent)),
		)
	}
	if m.staleDaemon {
		return oneLine(
			badgeWarnStyle.Render("⚠ Daemon 为旧版本") + " " +
				warnStyle.Render("运行 aio stop-daemon && aio 生效新功能"),
		)
	}
	if m.loading {
		return oneLine(lipgloss.NewStyle().Foreground(colorPrimary).Render("⏳ 处理中..."))
	}
	if inst, ok := m.backgroundRunningInstall(); ok {
		if m.mode == modeTerm {
			return oneLine(
				badgeShellStyle.Render("⏳ 安装中") + " " +
					softTextStyle.Render(fmt.Sprintf("%s 正在后台安装（%s 后按 ", inst.Name, m.escapeKeyLabel())) +
					keyHintStyle.Render(",") + softTextStyle.Render(" → ") + keyHintStyle.Render("1") +
					softTextStyle.Render(" 回到安装界面）"),
			)
		}
		return oneLine(
			badgeShellStyle.Render("⏳ 安装中") + " " +
				softTextStyle.Render(fmt.Sprintf("%s 正在后台安装 — 按 ", inst.Name)) +
				keyHintStyle.Render(",") + softTextStyle.Render(" → ") + keyHintStyle.Render("1") +
				softTextStyle.Render(" 随时回到安装界面"),
		)
	}

	var line string
	kh := func(k, label string) string {
		return keyHintStyle.Render(k) + " " + softTextStyle.Render(label)
	}
	if m.mode == modeTerm {
		badge := badgeTermStyle.Render("TERM")
		if m.shellMode {
			badge = badgeShellStyle.Render("SHELL")
		}
		shellLabel := "终端"
		if m.shellMode {
			shellLabel = "回Agent"
		}
		line = badge + "  " +
			kh("双击Esc/"+m.escapeKeyLabel(), "导航") + "  " +
			kh("Ctrl+\\", shellLabel) + "  " +
			kh("滚轮", "回看") + "  " +
			mutedStyle.Render("拖选复制 · 点左侧栏切导航")
		if m.toast != "" {
			line += "  " + dividerStyle.Render("│") + "  " + okStyle.Render(m.toast)
		}
		return lipgloss.NewStyle().MaxWidth(w).Render(ansi.Truncate(line, max(10, w), "…"))
	}
	if m.mode == modeDiff {
		line = badgeNavStyle.Render("DIFF") + "  " +
			kh("←/→", "切Agent") + "  " +
			kh("↑/↓", "滚屏") + "  " +
			kh("a", "采纳Winner") + "  " +
			kh("r", "刷新") + "  " +
			kh("Esc", "返回")
	} else {
		line = badgeNavStyle.Render("NAV") + "  " +
			kh("p", "目录") + "  " +
			kh("n", "任务") + "  " +
			kh("+", "加Agent") + "  " +
			kh("t", "终端") + "  " +
			kh("d", "Diff") + "  " +
			kh("a", "采纳") + "  " +
			kh("r", "Session") + "  " +
			kh("x", "删除") + "  " +
			kh(",", "控制中心") + "  " +
			kh("?", "帮助") + "  " +
			kh("q", "退出")
		if m.snap.Mobile.Running {
			line += "  " + okStyle.Render("● 手机同步中")
		}
		if m.toast != "" {
			line += "  " + dividerStyle.Render("│") + "  " + okStyle.Render(m.toast)
		}
	}
	return lipgloss.NewStyle().MaxWidth(w).Render(ansi.Truncate(line, max(10, w), "…"))
}

func (m Model) backgroundRunningInstall() (protocol.InstallSessionInfo, bool) {
	for _, inst := range m.snap.Installs {
		if !inst.Running {
			continue
		}
		if m.shellMode && m.shellID == inst.SessionID {
			continue
		}
		return inst, true
	}
	return protocol.InstallSessionInfo{}, false
}

func (m Model) waitingRuns() []protocol.Run {
	var out []protocol.Run
	for _, r := range m.snap.Runs {
		if r.Status != protocol.StatusWaitingInput {
			continue
		}
		// Don't nag in the bottom bar about the run the user is already actively viewing in TERM mode.
		if m.mode == modeTerm && r.ID == m.attachedRunID {
			continue
		}
		out = append(out, r)
	}
	return out
}

func joinLeftRight(left, right string, width int) string {
	if right == "" {
		return left
	}
	lw := ansi.StringWidth(left)
	rw := ansi.StringWidth(right)
	gap := width - lw - rw
	if gap < 1 {
		avail := width - rw - 1
		if avail <= 0 {
			return ansi.Truncate(left, max(1, width), "…")
		}
		return ansi.Truncate(left, avail, "…") + " " + right
	}
	return left + strings.Repeat(" ", gap) + right
}

func truncate(s string, maxLen int) string {
	if maxLen <= 2 {
		return s
	}
	if ansi.StringWidth(s) <= maxLen {
		return s
	}
	return ansi.Truncate(s, maxLen, "…")
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func formatSessionTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	local := t.Local()
	d := time.Since(local)
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%dm前", int(d.Minutes()))
	case d < 24*time.Hour:
		return local.Format("15:04")
	default:
		return local.Format("01-02 15:04")
	}
}
