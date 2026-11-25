package tui

import (
	"bytes"
	"fmt"
	"io"
	"sync"

	"github.com/charmbracelet/x/term"
)

// CursorSyncWriter wraps Bubble Tea's stdout (*os.File / term.File) so that at the end
// of every rendered frame, the host terminal's real hardware cursor is parked at the exact
// active input coordinates (1-based row/col). Implementing term.File preserves Bubble Tea's
// TTY detection, WindowSizeMsg queries, SIGWINCH handling, and color profile detection.
type CursorSyncWriter struct {
	out io.Writer

	mu     sync.Mutex
	active bool
	row    int // 1-based terminal row
	col    int // 1-based terminal col
}

var _ term.File = (*CursorSyncWriter)(nil)

func NewCursorSyncWriter(out io.Writer) *CursorSyncWriter {
	return &CursorSyncWriter{out: out}
}

func (w *CursorSyncWriter) Read(p []byte) (int, error) {
	if r, ok := w.out.(io.Reader); ok {
		return r.Read(p)
	}
	return 0, io.EOF
}

func (w *CursorSyncWriter) Close() error {
	if c, ok := w.out.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

func (w *CursorSyncWriter) Fd() uintptr {
	if f, ok := w.out.(interface{ Fd() uintptr }); ok {
		return f.Fd()
	}
	return 0
}

func (w *CursorSyncWriter) SetTarget(row, col int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if row < 1 || col < 1 {
		w.active = false
		return
	}
	w.active = true
	w.row = row
	w.col = col
}

func (w *CursorSyncWriter) Clear() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.active = false
}

func (w *CursorSyncWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	w.mu.Lock()
	active := w.active
	row := w.row
	col := w.col
	w.mu.Unlock()

	// Bubble Tea's alt-screen renderer always starts every frame flush with CursorHomePosition (\x1b[H).
	// Wrapping frame flushes in Synchronized Output (\x1b[?2026h ... \x1b[?2026l) + temporarily disabling
	// host auto-wrap (\x1b[?7l ... \x1b[?7h) guarantees:
	//   1. Atomic single-vsync presentation on WezTerm/Kitty/iTerm2/Ghostty/Windows Terminal (zero flicker/jump).
	//   2. Even if the host terminal renders an emoji/ambiguous symbol (e.g. ⚠, 🚀, flags) wider than
	//      Go's ansi.StringWidth, a line can NEVER wrap onto the next row and desync Bubble Tea's line-diff renderer.
	isFrame := bytes.HasPrefix(p, []byte("\x1b[H"))

	if !isFrame && !active {
		return w.out.Write(p)
	}

	var buf bytes.Buffer
	buf.Grow(len(p) + 48)
	if isFrame {
		buf.WriteString("\x1b[?2026h\x1b[?7l")
	}
	buf.Write(p)
	if isFrame {
		buf.WriteString("\x1b[?7h")
	}
	if active {
		fmt.Fprintf(&buf, "\x1b[%d;%dH", row, col)
	}
	if isFrame {
		buf.WriteString("\x1b[?2026l")
	}

	if _, err := w.out.Write(buf.Bytes()); err != nil {
		return 0, err
	}
	return len(p), nil
}
