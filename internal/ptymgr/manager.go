package ptymgr

import (
	"errors"
	"fmt"
	"image/color"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/loucongV/all-in-one/internal/adapter"
	"github.com/loucongV/all-in-one/internal/protocol"
	"github.com/loucongV/all-in-one/internal/quota"
)

const (
	defaultCols    = 120
	defaultRows    = 36
	maxRawRingSize = 256 * 1024
)

type StatusCallback func(runID string, status protocol.RunStatus, detail string, exitCode *int)

type Subscriber struct {
	Raw     bool
	Ch      chan []byte
	Screen  chan ScreenFrame
	closeFn func()
}

func (s *Subscriber) Close() {
	if s.closeFn != nil {
		s.closeFn()
	}
}

type ScreenFrame struct {
	ANSI         string
	Cursor       protocol.CursorPos
	ScrollOffset int
	ScrollMax    int
	Links        []protocol.LinkSpan
}

type Session struct {
	runID       string
	cmd         *exec.Cmd
	ptmx        *os.File
	emu         *vt.SafeEmulator
	vtCompat    vtStreamCompat
	onStatus    StatusCallback
	onRateLimit func(runID, detail string)
	onUsage     func(runID, plainScreen string)

	mu             sync.Mutex
	emuMu          sync.Mutex
	cols           int
	rows           int
	scrollOffset   int
	rawRing        []byte
	closed         bool
	exitCode       *int
	lastOutputAt   time.Time
	lastRateHitAt  time.Time
	lastUsageAt    time.Time
	syncStartedAt  time.Time
	lastTitle      string
	subs           map[*Subscriber]struct{}
	dirtyCh        chan struct{}
	doneCh         chan struct{}
	mouseModeMask  atomic.Uint32
	bracketedPaste atomic.Bool
}

func mouseModeBit(m ansi.Mode) uint32 {
	switch m {
	case ansi.ModeMouseX10:
		return 1 << 0
	case ansi.ModeMouseNormal:
		return 1 << 1
	case ansi.ModeMouseHighlight:
		return 1 << 2
	case ansi.ModeMouseButtonEvent:
		return 1 << 3
	case ansi.ModeMouseAnyEvent:
		return 1 << 4
	default:
		return 0
	}
}

type Manager struct {
	mu          sync.RWMutex
	sessions    map[string]*Session
	defaultCols int
	defaultRows int
	onStatus    StatusCallback
	onRateLimit func(runID, detail string)
	onUsage     func(runID, plainScreen string)
}

func NewManager(onStatus StatusCallback) *Manager {
	return &Manager{
		sessions:    make(map[string]*Session),
		defaultCols: defaultCols,
		defaultRows: defaultRows,
		onStatus:    onStatus,
	}
}

// SetDefaultSize records the active TUI viewport dimensions so newly spawned Agent and
// Shell PTYs open at the exact terminal size from byte 0 (preventing mid-boot resize
// corruption in Ink/Claude Code and tmux).
func (m *Manager) SetDefaultSize(cols, rows int) {
	if cols < 20 || rows < 6 {
		return
	}
	m.mu.Lock()
	m.defaultCols = cols
	m.defaultRows = rows
	m.mu.Unlock()
}

func (m *Manager) SetRateLimitCallback(fn func(runID, detail string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onRateLimit = fn
}

func (m *Manager) SetUsageCallback(fn func(runID, plainScreen string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onUsage = fn
}

func (m *Manager) Start(runID string, spec adapter.CommandSpec) (*Session, error) {
	m.mu.Lock()
	if existing, ok := m.sessions[runID]; ok {
		m.mu.Unlock()
		_ = existing.Stop()
		m.mu.Lock()
	}
	m.mu.Unlock()

	m.mu.RLock()
	initCols := m.defaultCols
	initRows := m.defaultRows
	rlCb := m.onRateLimit
	usageCb := m.onUsage
	m.mu.RUnlock()
	if initCols < 20 {
		initCols = defaultCols
	}
	if initRows < 6 {
		initRows = defaultRows
	}

	cmd := exec.Command(spec.Binary, spec.Args...)
	cmd.Dir = spec.Dir
	// Strip inherited TMUX/TMUX_PANE so shells launched inside aio can attach to tmux cleanly
	// without triggering nested-tmux warnings or broken inner terminal negotiation.
	baseChildEnv := append([]string{
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"-TMUX",
		"-TMUX_PANE",
	}, spec.Env...)
	cmd.Env = mergeEnv(os.Environ(), baseChildEnv)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Cols: uint16(initCols),
		Rows: uint16(initRows),
	})
	if err != nil {
		return nil, fmt.Errorf("start pty %s: %w", spec.Binary, err)
	}

	emu := vt.NewSafeEmulator(initCols, initRows)
	sess := &Session{
		runID:        runID,
		cmd:          cmd,
		ptmx:         ptmx,
		emu:          emu,
		onStatus:     m.onStatus,
		onRateLimit:  rlCb,
		onUsage:      usageCb,
		cols:         initCols,
		rows:         initRows,
		lastOutputAt: time.Now(),
		subs:         make(map[*Subscriber]struct{}),
		dirtyCh:      make(chan struct{}, 1),
		doneCh:       make(chan struct{}),
	}

	emu.SetCallbacks(vt.Callbacks{
		Bell: func() {
			sess.emitStatus(protocol.StatusWaitingInput, "terminal bell")
		},
		Title: func(title string) {
			sess.mu.Lock()
			sess.lastTitle = title
			sess.mu.Unlock()
			if status, detail, ok := classifyTerminalTitle(title); ok {
				sess.emitStatus(status, detail)
			}
		},
		CursorPosition: func(_, _ uv.Position) {
			select {
			case sess.dirtyCh <- struct{}{}:
			default:
			}
		},
		CursorVisibility: func(_ bool) {
			select {
			case sess.dirtyCh <- struct{}{}:
			default:
			}
		},
		EnableMode: func(mode ansi.Mode) {
			if mode == ansi.ModeBracketedPaste {
				sess.bracketedPaste.Store(true)
			}
			if bit := mouseModeBit(mode); bit != 0 {
				for {
					old := sess.mouseModeMask.Load()
					if sess.mouseModeMask.CompareAndSwap(old, old|bit) {
						break
					}
				}
			}
		},
		DisableMode: func(mode ansi.Mode) {
			if mode == ansi.ModeBracketedPaste {
				sess.bracketedPaste.Store(false)
			}
			if bit := mouseModeBit(mode); bit != 0 {
				for {
					old := sess.mouseModeMask.Load()
					if sess.mouseModeMask.CompareAndSwap(old, old&^bit) {
						break
					}
				}
			}
		},
	})
	emu.Focus()
	// Catch OSC 9 (iTerm/ConEmu notification) and OSC 777 (rxvt/desktop notification).
	emu.RegisterOscHandler(9, func(data []byte) bool {
		msg := strings.TrimSpace(string(data))
		lower := strings.ToLower(msg)
		if strings.Contains(lower, "ready for input") ||
			strings.Contains(lower, "finished") ||
			strings.Contains(lower, "completed") ||
			strings.Contains(lower, "turn complete") ||
			strings.Contains(lower, "agent-turn-complete") {
			sess.emitStatus(protocol.StatusDone, msg)
		} else {
			sess.emitStatus(protocol.StatusWaitingInput, "terminal notification")
		}
		return false
	})
	emu.RegisterOscHandler(777, func(data []byte) bool {
		if strings.HasPrefix(string(data), "notify;") {
			sess.emitStatus(protocol.StatusWaitingInput, "terminal notification")
		}
		return false
	})

	m.mu.Lock()
	m.sessions[runID] = sess
	m.mu.Unlock()

	go sess.pumpVTResponses()
	go sess.readLoop()
	go sess.screenBroadcastLoop()
	go sess.waitLoop()

	return sess, nil
}

func (m *Manager) Get(runID string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[runID]
	return s, ok
}

func (m *Manager) Stop(runID string) error {
	m.mu.RLock()
	s, ok := m.sessions[runID]
	m.mu.RUnlock()
	if !ok {
		return nil
	}
	return s.Stop()
}

// Remove stops the session (including its entire descendant process tree) and deletes
// it from the manager's active map so deleted runs release all memory and descriptors.
func (m *Manager) Remove(runID string) error {
	m.mu.Lock()
	s, ok := m.sessions[runID]
	delete(m.sessions, runID)
	m.mu.Unlock()
	if !ok {
		return nil
	}
	return s.Stop()
}

func (m *Manager) StopAll() {
	m.mu.RLock()
	all := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.RUnlock()
	for _, s := range all {
		_ = s.Stop()
	}
}

func (m *Manager) ActiveCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, s := range m.sessions {
		if !s.IsClosed() {
			n++
		}
	}
	return n
}

// SessionSummary describes a tracked PTY session ID and whether its process is still alive.
type SessionSummary struct {
	ID      string
	Running bool
}

// ListByPrefix returns all tracked PTY sessions whose ID begins with prefix, sorted by ID.
func (m *Manager) ListByPrefix(prefix string) []SessionSummary {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []SessionSummary
	for id, s := range m.sessions {
		if strings.HasPrefix(id, prefix) {
			out = append(out, SessionSummary{
				ID:      id,
				Running: !s.IsClosed(),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *Session) emitStatus(status protocol.RunStatus, detail string) {
	if s.onStatus != nil {
		s.onStatus(s.runID, status, detail, nil)
	}
}

// classifyTerminalTitle parses OSC 0/2 terminal window titles into normalized RunStatus
// transitions. Primarily powers zero-config real-time lifecycle tracking for Cursor CLI
// (which emits `<chat> - ✅ Ready`, `⏳ Working ...`, `❓ Waiting for you`,
// `🔐 Waiting for confirmation`, `📝 Reviewing changes`, `🧭 Planning`, `⌨️ Running shell command`, etc.)
// as well as generic CLIs that advertise waiting/approval prompts in their title.
func classifyTerminalTitle(title string) (protocol.RunStatus, string, bool) {
	t := strings.TrimSpace(title)
	if t == "" {
		return "", "", false
	}

	// Extract status segment after the last " - " if present (Cursor Agent format: "<name> - <status>").
	seg := t
	hasDashSeg := false
	if idx := strings.LastIndex(t, " - "); idx >= 0 {
		seg = strings.TrimSpace(t[idx+3:])
		hasDashSeg = true
	}
	lowerSeg := strings.ToLower(seg)
	lowerFull := strings.ToLower(t)

	// 1. Waiting for user input / tool approval / review.
	switch {
	case strings.Contains(seg, "🔐") || strings.Contains(lowerSeg, "waiting for confirmation"):
		return protocol.StatusWaitingInput, "title:waiting_confirmation", true
	case strings.Contains(seg, "❓") || strings.Contains(lowerSeg, "waiting for you"):
		return protocol.StatusWaitingInput, "title:waiting_user", true
	case strings.Contains(seg, "📝") || strings.Contains(lowerSeg, "reviewing changes"):
		return protocol.StatusWaitingInput, "title:reviewing", true
	case strings.Contains(lowerFull, "wait") || strings.Contains(lowerFull, "input") || strings.Contains(lowerFull, "approve"):
		return protocol.StatusWaitingInput, "title:waiting", true
	}

	// 2. Idle / Ready (completed turn).
	if strings.Contains(seg, "✅") || (hasDashSeg && (lowerSeg == "ready" || strings.HasSuffix(lowerSeg, " ready") || lowerSeg == "done" || lowerSeg == "idle")) {
		return protocol.StatusDone, "title:ready", true
	}

	// 3. Actively working / planning / running shell / queued / loading / reconnecting.
	switch {
	case strings.Contains(seg, "⏳") || (hasDashSeg && (lowerSeg == "working" || strings.HasPrefix(lowerSeg, "working "))):
		return protocol.StatusRunning, "title:working", true
	case strings.Contains(seg, "🧭") || (hasDashSeg && lowerSeg == "planning"):
		return protocol.StatusRunning, "title:planning", true
	case strings.Contains(seg, "⌨") || (hasDashSeg && strings.Contains(lowerSeg, "running shell")):
		return protocol.StatusRunning, "title:shell_running", true
	case strings.Contains(seg, "📋") || (hasDashSeg && lowerSeg == "queued"):
		return protocol.StatusRunning, "title:queued", true
	case strings.Contains(seg, "🔄") || (hasDashSeg && lowerSeg == "reconnecting"):
		return protocol.StatusRunning, "title:reconnecting", true
	case strings.Contains(seg, "📤") || (hasDashSeg && strings.Contains(lowerSeg, "moving to cloud")):
		return protocol.StatusRunning, "title:transfer", true
	}

	return "", "", false
}

// pumpVTResponses forwards VT query replies (CPR/DSR/DA) back to the PTY so CLIs never hang.
func (s *Session) pumpVTResponses() {
	buf := make([]byte, 1024)
	for {
		n, err := s.emu.Read(buf)
		if n > 0 {
			_, _ = s.ptmx.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (s *Session) readLoop() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			s.emuMu.Lock()
			wasSync := s.vtCompat.SyncActive()
			s.vtCompat.Write(s.emu, chunk)
			nowSync := s.vtCompat.SyncActive()
			s.emuMu.Unlock()

			s.mu.Lock()
			if nowSync && !wasSync {
				s.syncStartedAt = time.Now()
			} else if !nowSync {
				s.syncStartedAt = time.Time{}
			}
			s.mu.Unlock()

			if s.onRateLimit != nil {
				if detail, hit := quota.DetectRateLimit(chunk); hit {
					s.mu.Lock()
					shouldNotify := time.Since(s.lastRateHitAt) > 10*time.Second
					if shouldNotify {
						s.lastRateHitAt = time.Now()
					}
					s.mu.Unlock()
					if shouldNotify {
						s.onRateLimit(s.runID, detail)
					}
				}
			}

			s.mu.Lock()
			s.lastOutputAt = time.Now()
			s.appendRawLocked(chunk)
			for sub := range s.subs {
				if sub.Raw {
					select {
					case sub.Ch <- chunk:
					default:
					}
				}
			}
			s.mu.Unlock()

			select {
			case s.dirtyCh <- struct{}{}:
			default:
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				// Expected on Linux PTY when child exits (EIO).
			}
			return
		}
	}
}

func (s *Session) screenBroadcastLoop() {
	ticker := time.NewTicker(33 * time.Millisecond)
	defer ticker.Stop()

	dirty := false
	for {
		select {
		case <-s.doneCh:
			s.broadcastScreen()
			return
		case <-s.dirtyCh:
			dirty = true
		case <-ticker.C:
			if dirty {
				// Avoid snapshotting mid-frame during a ?2026h ... ?2026l synchronized update,
				// with a 150ms safety cap so a stuck app never freezes screen output.
				s.mu.Lock()
				holdForSync := !s.syncStartedAt.IsZero() && time.Since(s.syncStartedAt) < 150*time.Millisecond
				s.mu.Unlock()
				if holdForSync {
					continue
				}
				dirty = false
				s.broadcastScreen()
			}
		}
	}
}

func (s *Session) broadcastScreen() {
	frame := s.SnapshotScreen()
	s.mu.Lock()
	checkUsage := s.onUsage != nil && time.Since(s.lastUsageAt) > 1500*time.Millisecond
	if checkUsage {
		s.lastUsageAt = time.Now()
	}
	for sub := range s.subs {
		if !sub.Raw {
			select {
			case sub.Screen <- frame:
			default:
				// Replace stale frame so client always sees latest screen.
				select {
				case <-sub.Screen:
				default:
				}
				select {
				case sub.Screen <- frame:
				default:
				}
			}
		}
	}
	s.mu.Unlock()

	if checkUsage {
		if plain := ansi.Strip(frame.ANSI); plain != "" {
			s.onUsage(s.runID, plain)
		}
	}
}

func (s *Session) waitLoop() {
	err := s.cmd.Wait()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			code = 1
		}
	}

	s.mu.Lock()
	wasClosed := s.closed
	s.closed = true
	s.exitCode = &code
	s.mu.Unlock()

	close(s.doneCh)
	_ = s.ptmx.Close()
	if closer, ok := s.emu.InputPipe().(io.Closer); ok {
		_ = closer.Close()
	}

	if s.onStatus != nil {
		switch {
		case wasClosed:
			s.onStatus(s.runID, protocol.StatusStopped, "stopped", &code)
		case code == 0:
			s.onStatus(s.runID, protocol.StatusDone, "exited cleanly", &code)
		default:
			s.onStatus(s.runID, protocol.StatusError, fmt.Sprintf("exit code %d", code), &code)
		}
	}
}

func (s *Session) appendRawLocked(chunk []byte) {
	s.rawRing = append(s.rawRing, chunk...)
	if len(s.rawRing) > maxRawRingSize {
		s.rawRing = append([]byte(nil), s.rawRing[len(s.rawRing)-maxRawRingSize:]...)
	}
}

func (s *Session) WriteInput(data []byte) error {
	s.mu.Lock()
	closed := s.closed
	wasScrolled := s.scrollOffset > 0
	s.scrollOffset = 0
	s.mu.Unlock()
	if closed {
		return errors.New("session already exited")
	}
	if wasScrolled {
		select {
		case s.dirtyCh <- struct{}{}:
		default:
		}
	}
	_, err := s.ptmx.Write(data)
	return err
}

// Paste writes pasted text directly to the PTY, atomically wrapping it in bracketed paste
// markers (\x1b[200~...\x1b[201~) whenever the child CLI has enabled ?2004h.
// Normalizes CRLF -> CR (for Windows/SSH clients) and avoids vt.SafeEmulator lock contention.
func (s *Session) Paste(text string) error {
	s.mu.Lock()
	closed := s.closed
	wasScrolled := s.scrollOffset > 0
	s.scrollOffset = 0
	s.mu.Unlock()
	if closed {
		return errors.New("session already exited")
	}
	if wasScrolled {
		select {
		case s.dirtyCh <- struct{}{}:
		default:
		}
	}
	// Normalize Windows CRLF to CR and strip any stray bracketed-paste end marker.
	clean := strings.ReplaceAll(text, "\r\n", "\r")
	clean = strings.ReplaceAll(clean, ansi.BracketedPasteEnd, "")
	if clean == "" {
		return nil
	}
	var payload []byte
	if s.bracketedPaste.Load() {
		payload = make([]byte, 0, len(ansi.BracketedPasteStart)+len(clean)+len(ansi.BracketedPasteEnd))
		payload = append(payload, ansi.BracketedPasteStart...)
		payload = append(payload, clean...)
		payload = append(payload, ansi.BracketedPasteEnd...)
	} else {
		payload = []byte(clean)
	}
	_, err := s.ptmx.Write(payload)
	return err
}

// Scroll scrolls the terminal viewport up (delta > 0) or down (delta < 0).
// If the child CLI is in alternate screen (e.g. vim/less/htop or legacy alt-screen TUI),
// wheel events are forwarded directly to the child app (falling back to Up/Down keys when
// mouse reporting is not enabled); otherwise it scrolls the 10,000-line VT scrollback buffer.
func (s *Session) Scroll(delta, relX, relY int) {
	if s.emu.IsAltScreen() {
		steps := delta
		up := true
		if delta < 0 {
			up = false
			steps = -delta
		}
		if steps > 5 {
			steps = 5
		}
		if steps == 0 {
			return
		}
		if s.mouseModeMask.Load() == 0 {
			// Alternate screen without mouse tracking (e.g. less, man, git diff):
			// synthesize Up/Down arrow keys (xterm Alternate Scroll Mode behavior).
			seq := []byte("\x1b[A")
			if !up {
				seq = []byte("\x1b[B")
			}
			for i := 0; i < steps; i++ {
				_, _ = s.ptmx.Write(seq)
			}
			return
		}
		s.mu.Lock()
		cols, rows := s.cols, s.rows
		s.mu.Unlock()
		if (relX <= 0 && relY <= 0) || relX < 0 || relX >= cols {
			relX = max(0, cols/2)
		}
		if relY < 0 || relY >= rows {
			relY = max(0, rows/3)
		}
		btn := uv.MouseWheelUp
		if !up {
			btn = uv.MouseWheelDown
		}
		for i := 0; i < steps; i++ {
			s.emu.SendMouse(uv.MouseWheelEvent{
				X:      relX,
				Y:      relY,
				Button: btn,
			})
		}
		return
	}

	sbLen := s.emu.ScrollbackLen()
	s.mu.Lock()
	next := s.scrollOffset + delta
	if next < 0 {
		next = 0
	}
	if next > sbLen {
		next = sbLen
	}
	changed := next != s.scrollOffset
	s.scrollOffset = next
	s.mu.Unlock()

	if changed {
		select {
		case s.dirtyCh <- struct{}{}:
		default:
		}
	}
}

func (s *Session) ResetScroll() {
	s.mu.Lock()
	changed := s.scrollOffset != 0
	s.scrollOffset = 0
	s.mu.Unlock()
	if changed {
		select {
		case s.dirtyCh <- struct{}{}:
		default:
		}
	}
}

func (s *Session) Resize(cols, rows int) error {
	if cols < 20 {
		cols = 20
	}
	if rows < 6 {
		rows = 6
	}
	s.mu.Lock()
	if s.closed || (s.cols == cols && s.rows == rows) {
		s.mu.Unlock()
		return nil
	}
	s.cols = cols
	s.rows = rows
	s.mu.Unlock()

	if err := pty.Setsize(s.ptmx, &pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
	}); err != nil {
		return err
	}
	s.emuMu.Lock()
	s.emu.Resize(cols, rows)
	s.emuMu.Unlock()
	select {
	case s.dirtyCh <- struct{}{}:
	default:
	}
	return nil
}

func (s *Session) SnapshotScreen() ScreenFrame {
	s.emuMu.Lock()
	defer s.emuMu.Unlock()

	w := s.emu.Width()
	h := s.emu.Height()
	pos := s.emu.CursorPosition()
	hidden := s.emu.CursorHidden()
	sbLen := s.emu.ScrollbackLen()

	s.mu.Lock()
	offset := s.scrollOffset
	if offset > sbLen {
		offset = sbLen
		s.scrollOffset = offset
	}
	s.mu.Unlock()

	rendered := ""
	var links []protocol.LinkSpan
	if w > 0 && h > 0 {
		buf := uv.NewScreenBuffer(w, h)
		if offset > 0 && !s.emu.IsAltScreen() {
			// Render scrolled viewport combining VT scrollback + live screen rows.
			// Advance x by cell.Width so trailing continuation cells of wide CJK/emoji
			// characters never overwrite and erase their leading cell.
			for y := 0; y < h; y++ {
				vLine := sbLen - offset + y
				for x := 0; x < w; {
					step := 1
					var cell *uv.Cell
					if vLine < sbLen {
						cell = s.emu.ScrollbackCellAt(x, vLine)
					} else {
						cell = s.emu.CellAt(x, vLine-sbLen)
					}
					if cell != nil && !cell.IsZero() {
						if cell.Width > 1 {
							step = cell.Width
						}
						buf.SetCell(x, y, cell.Clone())
					}
					x += step
				}
			}
			hidden = true
		} else {
			// Copy live cells directly instead of s.emu.Draw() so untouched cells stay transparent
			// (uv.EmptyCell with Bg==nil) instead of flooding every row with TrueColor black background
			// (\x1b[48;2;0;0;0m), which breaks non-RGB tmux and bloats SSH frames.
			for y := 0; y < h; y++ {
				for x := 0; x < w; {
					step := 1
					if cell := s.emu.CellAt(x, y); cell != nil && !cell.IsZero() {
						if cell.Width > 1 {
							step = cell.Width
						}
						buf.SetCell(x, y, cell.Clone())
					}
					x += step
				}
			}
			if !hidden && pos.X >= 0 && pos.X < w && pos.Y >= 0 && pos.Y < h {
				var c *uv.Cell
				if existing := buf.CellAt(pos.X, pos.Y); existing != nil {
					c = existing.Clone()
				} else {
					c = &uv.Cell{}
				}
				if c.Content == "" {
					c.Content = " "
					c.Width = 1
				}
				// High-contrast block cursor (bright light block + dark text) so it stays crisp
				// even over dim placeholder text or dark input boxes.
				c.Style.Attrs &^= uv.AttrFaint | uv.AttrReverse
				c.Style.Fg = color.RGBA{R: 15, G: 23, B: 42, A: 255}
				c.Style.Bg = color.RGBA{R: 226, G: 232, B: 240, A: 255}
				buf.SetCell(pos.X, pos.Y, c)
			}
		}
		links = extractViewportLinks(&buf, w, h)
		rendered = buf.Render()
	} else {
		rendered = s.emu.Render()
	}

	return ScreenFrame{
		ANSI: rendered,
		Cursor: protocol.CursorPos{
			X:      pos.X,
			Y:      pos.Y,
			Hidden: hidden,
		},
		ScrollOffset: offset,
		ScrollMax:    sbLen,
		Links:        links,
	}
}

// SnapshotFullScreen renders up to maxHistoryLines of VT scrollback followed by the live
// screen into a single ANSI frame so mobile web clients can scroll natively through the full
// conversation history without stepping through viewport-sized scroll commands.
func (s *Session) SnapshotFullScreen(maxHistoryLines int) ScreenFrame {
	s.emuMu.Lock()
	defer s.emuMu.Unlock()

	w := s.emu.Width()
	h := s.emu.Height()
	pos := s.emu.CursorPosition()
	hidden := s.emu.CursorHidden()
	sbLen := s.emu.ScrollbackLen()

	if w <= 0 || h <= 0 || s.emu.IsAltScreen() || sbLen == 0 {
		rendered := s.emu.Render()
		return ScreenFrame{
			ANSI: rendered,
			Cursor: protocol.CursorPos{
				X:      pos.X,
				Y:      pos.Y,
				Hidden: hidden,
			},
			ScrollMax: sbLen,
		}
	}

	histCount := sbLen
	if maxHistoryLines > 0 && histCount > maxHistoryLines {
		histCount = maxHistoryLines
	}
	startLine := sbLen - histCount
	totalH := histCount + h
	buf := uv.NewScreenBuffer(w, totalH)

	for y := 0; y < histCount; y++ {
		sbY := startLine + y
		for x := 0; x < w; {
			step := 1
			if cell := s.emu.ScrollbackCellAt(x, sbY); cell != nil && !cell.IsZero() {
				if cell.Width > 1 {
					step = cell.Width
				}
				buf.SetCell(x, y, cell.Clone())
			}
			x += step
		}
	}
	for y := 0; y < h; y++ {
		dstY := histCount + y
		for x := 0; x < w; {
			step := 1
			if cell := s.emu.CellAt(x, y); cell != nil && !cell.IsZero() {
				if cell.Width > 1 {
					step = cell.Width
				}
				buf.SetCell(x, dstY, cell.Clone())
			}
			x += step
		}
	}

	return ScreenFrame{
		ANSI: buf.Render(),
		Cursor: protocol.CursorPos{
			X:      pos.X,
			Y:      histCount + pos.Y,
			Hidden: hidden,
		},
		ScrollMax: sbLen,
	}
}

var plainURLRe = regexp.MustCompile(`https?://[^\s<>"'\)\]\},;]+`)

// extractViewportLinks collects both OSC 8 terminal hyperlinks (e.g. markdown links like "商品页")
// and plain-text http/https URLs from the rendered viewport buffer.
func extractViewportLinks(buf *uv.ScreenBuffer, w, h int) []protocol.LinkSpan {
	var links []protocol.LinkSpan
	for y := 0; y < h; y++ {
		// 1. Collect OSC 8 cell hyperlinks.
		curURL := ""
		startX := -1
		endX := -1
		flushOSC8 := func() {
			if curURL != "" && startX >= 0 {
				links = append(links, protocol.LinkSpan{
					Row:    y,
					StartX: startX,
					EndX:   endX,
					URL:    curURL,
				})
			}
			curURL = ""
			startX = -1
			endX = -1
		}

		// Build plain row text + column map for fallback plain-URL matching.
		var rowText strings.Builder
		var byteCol []int
		hasOSC8 := make([]bool, w)

		for x := 0; x < w; x++ {
			cell := buf.CellAt(x, y)
			cellURL := ""
			ch := " "
			cw := 1
			if cell != nil {
				cellURL = strings.TrimSpace(cell.Link.URL)
				if cell.Content != "" {
					ch = cell.Content
				}
				if cell.Width > 0 {
					cw = cell.Width
				}
			}
			if cellURL != "" {
				hasOSC8[x] = true
				if cw == 2 && x+1 < w {
					hasOSC8[x+1] = true
				}
				if cellURL == curURL && x <= endX+1 {
					endX = x + cw - 1
				} else {
					flushOSC8()
					curURL = cellURL
					startX = x
					endX = x + cw - 1
				}
			} else if curURL != "" && x > endX {
				flushOSC8()
			}

			// Skip trailing placeholder cell of a wide character (Width == 0).
			if cell != nil && cell.Width == 0 {
				continue
			}
			for range len(ch) {
				byteCol = append(byteCol, x)
			}
			rowText.WriteString(ch)
		}
		flushOSC8()

		// 2. Collect plain-text http/https URLs not already covered by OSC 8.
		txt := rowText.String()
		if !strings.Contains(txt, "http://") && !strings.Contains(txt, "https://") {
			continue
		}
		for _, loc := range plainURLRe.FindAllStringIndex(txt, -1) {
			if loc[0] >= len(byteCol) || loc[1]-1 >= len(byteCol) {
				continue
			}
			sx := byteCol[loc[0]]
			ex := byteCol[loc[1]-1]
			if sx < w && hasOSC8[sx] {
				continue
			}
			u := strings.TrimRight(txt[loc[0]:loc[1]], ".,;:!?)")
			if u != "" {
				links = append(links, protocol.LinkSpan{
					Row:    y,
					StartX: sx,
					EndX:   ex,
					URL:    u,
				})
			}
		}
	}
	return links
}

func (s *Session) RawHistory() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.rawRing...)
}

func (s *Session) IsClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// Done returns a channel that is closed when the underlying PTY process exits.
func (s *Session) Done() <-chan struct{} {
	return s.doneCh
}

func (s *Session) Subscribe(raw bool) *Subscriber {
	sub := &Subscriber{
		Raw: raw,
	}
	if raw {
		sub.Ch = make(chan []byte, 64)
	} else {
		sub.Screen = make(chan ScreenFrame, 2)
	}
	s.mu.Lock()
	s.subs[sub] = struct{}{}
	s.mu.Unlock()

	sub.closeFn = func() {
		s.mu.Lock()
		delete(s.subs, sub)
		s.mu.Unlock()
	}
	return sub
}

func (s *Session) Stop() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	proc := s.cmd.Process
	s.mu.Unlock()

	if proc != nil {
		// Signal the entire descendant process tree (including detached MCP servers / subcommands)
		// gracefully with SIGTERM, escalating to SIGKILL after 1.5s.
		signalProcessTree(proc.Pid, syscall.SIGTERM)
		select {
		case <-s.doneCh:
			// Sweep any lingering detached descendants after root exits.
			signalProcessTree(proc.Pid, syscall.SIGKILL)
			return nil
		case <-time.After(1500 * time.Millisecond):
			signalProcessTree(proc.Pid, syscall.SIGKILL)
			_ = proc.Kill()
			<-s.doneCh
		}
	}
	return nil
}

// mergeEnv merges override entries on top of base environment variables.
// An override entry prefixed with "-" (e.g. "-HTTP_PROXY") explicitly removes that key
// from the resulting environment (used for per-agent direct/no-proxy routing).
func mergeEnv(base, overrides []string) []string {
	m := make(map[string]string, len(base)+len(overrides))
	order := make([]string, 0, len(base)+len(overrides))
	unset := make(map[string]bool)
	add := func(kv string) {
		if strings.HasPrefix(kv, "-") && !strings.Contains(kv, "=") {
			k := strings.TrimPrefix(kv, "-")
			delete(m, k)
			unset[k] = true
			return
		}
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return
		}
		delete(unset, k)
		if _, exists := m[k]; !exists {
			order = append(order, k)
		}
		m[k] = v
	}
	for _, kv := range base {
		add(kv)
	}
	for _, kv := range overrides {
		add(kv)
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		if unset[k] {
			continue
		}
		if v, ok := m[k]; ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}
