package ptymgr

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

func TestVTStreamCompatWideCharRightMarginWrap(t *testing.T) {
	// Width = 10: "12345678" (8 cols) + "中" (2 cols -> fills cols 8..9) + "文字" (4 cols -> must wrap cleanly to row 1).
	// Without vtStreamCompat, x/vt@v0.1.0 leaves atPhantom=false at col 9, erasing "中" and dropping "文".
	emu := vt.NewSafeEmulator(10, 4)
	var compat vtStreamCompat
	compat.Write(emu, []byte("12345678中文字"))

	plain := ansi.Strip(emu.Render())
	lines := strings.Split(plain, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines, got %q", plain)
	}
	if got := strings.TrimSpace(lines[0]); got != "12345678中" {
		t.Fatalf("row 0 corrupted: want %q, got %q", "12345678中", got)
	}
	if got := strings.TrimSpace(lines[1]); got != "文字" {
		t.Fatalf("row 1 corrupted: want %q, got %q", "文字", got)
	}

	// Trailing CRLF immediately after a right-margin CJK char MUST NOT double-wrap.
	emu2 := vt.NewSafeEmulator(10, 4)
	var compat2 vtStreamCompat
	compat2.Write(emu2, []byte("12345678中\r\n下"))
	lines2 := strings.Split(ansi.Strip(emu2.Render()), "\n")
	if got := strings.TrimSpace(lines2[0]); got != "12345678中" {
		t.Fatalf("row 0 unexpected: %q", got)
	}
	if got := strings.TrimSpace(lines2[1]); got != "下" {
		t.Fatalf("expected single wrap on explicit CRLF, got row1=%q row2=%q", lines2[1], lines2[2])
	}
}

func TestVTStreamCompatRestoresCursorOnCSIuAndTracksSyncOutput(t *testing.T) {
	emu := vt.NewSafeEmulator(20, 4)
	var compat vtStreamCompat

	// Save cursor at (4, 0) via CSI s, move to row 2, write "B", restore via CSI u (split across chunks), write "A".
	compat.Write(emu, []byte("1234\x1b[s\x1b[3;1HB\x1b["))
	compat.Write(emu, []byte("uA"))

	lines := strings.Split(ansi.Strip(emu.Render()), "\n")
	if got := strings.TrimSpace(lines[0]); got != "1234A" {
		t.Fatalf("expected CSI u to restore cursor to col 4 and write 'A', got %q", got)
	}
	if got := strings.TrimSpace(lines[2]); got != "B" {
		t.Fatalf("expected 'B' on row 2, got %q", got)
	}

	// Synchronized output (?2026h / ?2026l) tracking across chunks.
	compat.Write(emu, []byte("\x1b[?2026h"))
	if !compat.SyncActive() {
		t.Fatalf("expected SyncActive()=true after ?2026h")
	}
	compat.Write(emu, []byte("\x1b[?2026l"))
	if compat.SyncActive() {
		t.Fatalf("expected SyncActive()=false after ?2026l")
	}
}
