package ptymgr

import (
	"bytes"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// vtStreamCompat wraps writes into charmbracelet/x/vt@v0.1.0 to work around three real-world
// terminal emulation edge cases encountered with Claude Code (Ink), Codex, and tmux over SSH:
//
//  1. Missing CSI u (SCORC - Restore Cursor): x/vt v0.1.0 handles CSI s (SCOSC) and ESC 7/8,
//     but drops bare CSI u (\x1b[u). We translate bare CSI u -> ESC 8 (\x1b8) while preserving
//     Kitty keyboard protocol sequences (\x1b[>1u, \x1b[<u, \x1b[?u, \x1b[=1;1u).
//  2. Wide-character right-margin wrap (x == width-2 + 2-cell CJK/emoji): x/vt v0.1.0 only sets
//     its pending-wrap phantom flag when x >= width-1, so a 2-cell CJK glyph landing on the last
//     two columns leaves cursor at width-1 without pending-wrap; the next glyph then overwrites
//     the trailing cell (erasing both glyphs) and skips a line wrap, causing Ink/Claude Code's
//     cursor-up offsets to drift and overlap rows. We track pendingWideWrap and emit CRLF before
//     the next printable glyph (clearing it on any control/escape sequence, matching VT100 DECAWM).
//  3. Synchronized Output (?2026h / ?2026l): tracks active sync-update batches so screen snapshots
//     wait for ?2026l instead of broadcasting torn mid-redraw frames.
type vtStreamCompat struct {
	pending         []byte
	pendingWideWrap bool
	syncActive      bool
}

// Write processes raw PTY output bytes into emu while maintaining state across chunk boundaries.
// Caller MUST hold emuMu.
func (c *vtStreamCompat) Write(emu *vt.SafeEmulator, chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	var data []byte
	if len(c.pending) > 0 {
		data = make([]byte, 0, len(c.pending)+len(chunk))
		data = append(data, c.pending...)
		data = append(data, chunk...)
		c.pending = c.pending[:0]
	} else {
		data = chunk
	}

	i := 0
	n := len(data)
	for i < n {
		b := data[i]

		// 1. Escape sequence handling (ESC ...).
		if b == 0x1b {
			seqLen, complete := scanEscapeSeq(data[i:])
			if !complete {
				c.pending = append(c.pending[:0], data[i:]...)
				return
			}
			seq := data[i : i+seqLen]
			c.pendingWideWrap = false
			if bytes.Equal(seq, []byte("\x1b[u")) {
				// Translate bare CSI u (SCORC) to ESC 8 (DECRC).
				_, _ = emu.Write([]byte("\x1b8"))
			} else {
				if bytes.Equal(seq, []byte("\x1b[?2026h")) {
					c.syncActive = true
				} else if bytes.Equal(seq, []byte("\x1b[?2026l")) {
					c.syncActive = false
				}
				_, _ = emu.Write(seq)
			}
			i += seqLen
			continue
		}

		// 2. C0 control characters (CR, LF, BS, TAB, BEL, etc.) clear pending wide wrap.
		if b < 0x20 || b == 0x7f {
			c.pendingWideWrap = false
			_, _ = emu.Write(data[i : i+1])
			i++
			continue
		}

		// 3. Fast path for printable ASCII run (width 1 each) when no pendingWideWrap.
		if b < 0x80 {
			if c.pendingWideWrap {
				c.pendingWideWrap = false
				_, _ = emu.Write([]byte("\r\n"))
			}
			start := i
			for i < n && data[i] >= 0x20 && data[i] < 0x7f {
				i++
			}
			_, _ = emu.Write(data[start:i])
			continue
		}

		// 4. Multi-byte UTF-8 rune: wait for complete rune across chunk boundary.
		if !utf8.FullRune(data[i:]) {
			if n-i < utf8.UTFMax {
				c.pending = append(c.pending[:0], data[i:]...)
				return
			}
			// Invalid UTF-8 byte: pass through so parser advances.
			_, _ = emu.Write(data[i : i+1])
			i++
			continue
		}

		r, size := utf8.DecodeRune(data[i:])
		if c.pendingWideWrap {
			c.pendingWideWrap = false
			_, _ = emu.Write([]byte("\r\n"))
		}

		rw := ansi.StringWidth(string(r))
		w := emu.Width()
		if rw > 1 && w >= 2 {
			pos := emu.CursorPosition()
			if pos.X == w-1 {
				// Only 1 column remains on this row; wrap before placing a 2-column glyph
				// so x/vt doesn't silently discard it as out-of-bounds.
				_, _ = emu.Write([]byte("\r\n"))
				pos = emu.CursorPosition()
			}
			_, _ = emu.Write(data[i : i+size])
			if pos.X+rw >= w {
				c.pendingWideWrap = true
			}
		} else {
			_, _ = emu.Write(data[i : i+size])
		}
		i += size
	}
}

// SyncActive reports whether the child app is currently inside a ?2026h ... ?2026l sync frame.
func (c *vtStreamCompat) SyncActive() bool {
	return c.syncActive
}

// scanEscapeSeq inspects an ESC-prefixed sequence at data[0] == 0x1b.
// Returns (length, true) when a complete sequence is present, or (0, false) if truncated at EOF.
func scanEscapeSeq(data []byte) (int, bool) {
	n := len(data)
	if n < 2 {
		return 0, false
	}
	switch data[1] {
	case '[': // CSI: ESC [ <param 0x30-0x3F>* <interm 0x20-0x2F>* <final 0x40-0x7E>
		for j := 2; j < n; j++ {
			if data[j] >= 0x40 && data[j] <= 0x7e {
				return j + 1, true
			}
			if j > 64 {
				return j + 1, true
			}
		}
		return 0, false
	case ']': // OSC: terminated by BEL (0x07) or ST (ESC \)
		for j := 2; j < n; j++ {
			if data[j] == 0x07 {
				return j + 1, true
			}
			if data[j] == 0x1b {
				if j+1 >= n {
					return 0, false
				}
				if data[j+1] == '\\' {
					return j + 2, true
				}
			}
			if j > 8192 {
				return j + 1, true
			}
		}
		return 0, false
	case 'P', '^', '_', 'X': // DCS / PM / APC / SOS: terminated by ST (ESC \)
		for j := 2; j < n; j++ {
			if data[j] == 0x1b {
				if j+1 >= n {
					return 0, false
				}
				if data[j+1] == '\\' {
					return j + 2, true
				}
			}
			if j > 8192 {
				return j + 1, true
			}
		}
		return 0, false
	case '(', ')', '*', '+': // Character set selection (ESC ( 0, etc.)
		if n < 3 {
			return 0, false
		}
		return 3, true
	default:
		// 2-byte ESC sequence (ESC 7, ESC 8, ESC M, ESC =, ESC >, etc.)
		return 2, true
	}
}
