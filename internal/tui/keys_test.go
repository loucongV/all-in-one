package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestEncodeKeyToPTYModifierArrowsAndSpecialKeys(t *testing.T) {
	cases := []struct {
		name string
		msg  tea.KeyMsg
		want string
	}{
		{"shift+left", tea.KeyMsg{Type: tea.KeyShiftLeft}, "\x1b[1;2D"},
		{"shift+right", tea.KeyMsg{Type: tea.KeyShiftRight}, "\x1b[1;2C"},
		{"shift+up", tea.KeyMsg{Type: tea.KeyShiftUp}, "\x1b[1;2A"},
		{"shift+down", tea.KeyMsg{Type: tea.KeyShiftDown}, "\x1b[1;2B"},
		{"ctrl+left", tea.KeyMsg{Type: tea.KeyCtrlLeft}, "\x1b[1;5D"},
		{"ctrl+shift+right", tea.KeyMsg{Type: tea.KeyCtrlShiftRight}, "\x1b[1;6C"},
		{"alt+left", tea.KeyMsg{Type: tea.KeyLeft, Alt: true}, "\x1b[1;3D"},
		{"alt+enter", tea.KeyMsg{Type: tea.KeyEnter, Alt: true}, "\x1b\r"},
		{"shift+home", tea.KeyMsg{Type: tea.KeyShiftHome}, "\x1b[1;2H"},
		{"f2", tea.KeyMsg{Type: tea.KeyF2}, "\x1bOQ"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(encodeKeyToPTY(tc.msg)); got != tc.want {
				t.Fatalf("encodeKeyToPTY(%s) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}
