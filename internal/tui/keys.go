package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// encodeKeyToPTY converts a Bubble Tea KeyMsg into raw ANSI/VT bytes suitable for writing to a PTY.
func encodeKeyToPTY(msg tea.KeyMsg) []byte {
	switch msg.Type {
	case tea.KeyEnter:
		if msg.Alt {
			return []byte("\x1b\r")
		}
		return []byte{'\r'}
	case tea.KeyBackspace:
		if msg.Alt {
			return []byte{0x1b, 0x7f}
		}
		return []byte{0x7f}
	case tea.KeyTab:
		return []byte{'\t'}
	case tea.KeyShiftTab:
		return []byte("\x1b[Z")
	case tea.KeyEsc:
		return []byte{0x1b}
	case tea.KeySpace:
		if msg.Alt {
			return []byte{0x1b, ' '}
		}
		return []byte{' '}

	// Cursor arrows (plain + Alt/Shift/Ctrl/Ctrl+Shift xterm CSI modifier sequences).
	case tea.KeyUp:
		if msg.Alt {
			return []byte("\x1b[1;3A")
		}
		return []byte("\x1b[A")
	case tea.KeyDown:
		if msg.Alt {
			return []byte("\x1b[1;3B")
		}
		return []byte("\x1b[B")
	case tea.KeyRight:
		if msg.Alt {
			return []byte("\x1b[1;3C")
		}
		return []byte("\x1b[C")
	case tea.KeyLeft:
		if msg.Alt {
			return []byte("\x1b[1;3D")
		}
		return []byte("\x1b[D")
	case tea.KeyShiftUp:
		if msg.Alt {
			return []byte("\x1b[1;4A")
		}
		return []byte("\x1b[1;2A")
	case tea.KeyShiftDown:
		if msg.Alt {
			return []byte("\x1b[1;4B")
		}
		return []byte("\x1b[1;2B")
	case tea.KeyShiftRight:
		if msg.Alt {
			return []byte("\x1b[1;4C")
		}
		return []byte("\x1b[1;2C")
	case tea.KeyShiftLeft:
		if msg.Alt {
			return []byte("\x1b[1;4D")
		}
		return []byte("\x1b[1;2D")
	case tea.KeyCtrlUp:
		return []byte("\x1b[1;5A")
	case tea.KeyCtrlDown:
		return []byte("\x1b[1;5B")
	case tea.KeyCtrlRight:
		return []byte("\x1b[1;5C")
	case tea.KeyCtrlLeft:
		return []byte("\x1b[1;5D")
	case tea.KeyCtrlShiftUp:
		return []byte("\x1b[1;6A")
	case tea.KeyCtrlShiftDown:
		return []byte("\x1b[1;6B")
	case tea.KeyCtrlShiftRight:
		return []byte("\x1b[1;6C")
	case tea.KeyCtrlShiftLeft:
		return []byte("\x1b[1;6D")

	// Home / End / PageUp / PageDown / Delete / Insert.
	case tea.KeyHome:
		if msg.Alt {
			return []byte("\x1b[1;3H")
		}
		return []byte("\x1b[H")
	case tea.KeyEnd:
		if msg.Alt {
			return []byte("\x1b[1;3F")
		}
		return []byte("\x1b[F")
	case tea.KeyShiftHome:
		return []byte("\x1b[1;2H")
	case tea.KeyShiftEnd:
		return []byte("\x1b[1;2F")
	case tea.KeyCtrlHome:
		return []byte("\x1b[1;5H")
	case tea.KeyCtrlEnd:
		return []byte("\x1b[1;5F")
	case tea.KeyCtrlShiftHome:
		return []byte("\x1b[1;6H")
	case tea.KeyCtrlShiftEnd:
		return []byte("\x1b[1;6F")
	case tea.KeyPgUp:
		return []byte("\x1b[5~")
	case tea.KeyPgDown:
		return []byte("\x1b[6~")
	case tea.KeyCtrlPgUp:
		return []byte("\x1b[5;5~")
	case tea.KeyCtrlPgDown:
		return []byte("\x1b[6;5~")
	case tea.KeyDelete:
		if msg.Alt {
			return []byte("\x1b[3;3~")
		}
		return []byte("\x1b[3~")
	case tea.KeyInsert:
		return []byte("\x1b[2~")

	// Function keys F1..F12.
	case tea.KeyF1:
		return []byte("\x1bOP")
	case tea.KeyF2:
		return []byte("\x1bOQ")
	case tea.KeyF3:
		return []byte("\x1bOR")
	case tea.KeyF4:
		return []byte("\x1bOS")
	case tea.KeyF5:
		return []byte("\x1b[15~")
	case tea.KeyF6:
		return []byte("\x1b[17~")
	case tea.KeyF7:
		return []byte("\x1b[18~")
	case tea.KeyF8:
		return []byte("\x1b[19~")
	case tea.KeyF9:
		return []byte("\x1b[20~")
	case tea.KeyF10:
		return []byte("\x1b[21~")
	case tea.KeyF11:
		return []byte("\x1b[23~")
	case tea.KeyF12:
		return []byte("\x1b[24~")

	case tea.KeyRunes:
		s := string(msg.Runes)
		if msg.Alt {
			return append([]byte{0x1b}, []byte(s)...)
		}
		return []byte(s)
	}

	// Control characters Ctrl+A (0x01) .. Ctrl+Z (0x1a).
	if msg.Type >= tea.KeyCtrlA && msg.Type <= tea.KeyCtrlZ {
		return []byte{byte(msg.Type)}
	}

	switch msg.String() {
	case "ctrl+c":
		return []byte{0x03}
	case "ctrl+d":
		return []byte{0x04}
	case "ctrl+l":
		return []byte{0x0c}
	case "ctrl+u":
		return []byte{0x15}
	case "ctrl+w":
		return []byte{0x17}
	case "ctrl+z":
		return []byte{0x1a}
	case "shift+left":
		return []byte("\x1b[1;2D")
	case "shift+right":
		return []byte("\x1b[1;2C")
	case "shift+up":
		return []byte("\x1b[1;2A")
	case "shift+down":
		return []byte("\x1b[1;2B")
	}
	return nil
}
