package tui

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/term"
)

func TestCursorSyncWriterAppendsCUPWhenActive(t *testing.T) {
	var out bytes.Buffer
	cw := NewCursorSyncWriter(&out)

	// Verify term.File interface delegation when wrapping an *os.File.
	var tf term.File = NewCursorSyncWriter(os.Stdout)
	if tf.Fd() != os.Stdout.Fd() {
		t.Fatalf("expected delegated Fd %d, got %d", os.Stdout.Fd(), tf.Fd())
	}

	// Inactive: passes bytes through unchanged.
	n, err := cw.Write([]byte("hello"))
	if err != nil || n != 5 || out.String() != "hello" {
		t.Fatalf("inactive write mismatch: n=%d err=%v out=%q", n, err, out.String())
	}

	// Active: appends \x1b[row;colH and reports original byte count to caller.
	out.Reset()
	cw.SetTarget(36, 39)
	n, err = cw.Write([]byte("frame"))
	if err != nil || n != 5 {
		t.Fatalf("active write returned n=%d err=%v", n, err)
	}
	if !strings.HasSuffix(out.String(), "\x1b[36;39H") {
		t.Fatalf("expected CUP suffix \\x1b[36;39H, got %q", out.String())
	}

	// Cleared: back to pass-through.
	out.Reset()
	cw.Clear()
	_, _ = cw.Write([]byte("bye"))
	if out.String() != "bye" {
		t.Fatalf("expected plain write after Clear, got %q", out.String())
	}
}

func TestCursorSyncWriterWrapsFramesWithSyncOutputAndNoWrap(t *testing.T) {
	var out bytes.Buffer
	cw := NewCursorSyncWriter(&out)

	// Alt-screen frame flush starts with CursorHomePosition (\x1b[H).
	frame := []byte("\x1b[Hrow1\r\nrow2")
	cw.SetTarget(12, 34)
	n, err := cw.Write(frame)
	if err != nil || n != len(frame) {
		t.Fatalf("frame write returned n=%d err=%v", n, err)
	}
	got := out.String()
	want := "\x1b[?2026h\x1b[?7l" + string(frame) + "\x1b[?7h\x1b[12;34H\x1b[?2026l"
	if got != want {
		t.Fatalf("frame wrapping mismatch:\ngot:  %q\nwant: %q", got, want)
	}

	// Even when cursor sync is cleared (e.g. NAV mode), frame flushes still use SyncOutput + DECAWM guard.
	out.Reset()
	cw.Clear()
	_, _ = cw.Write(frame)
	wantNoCursor := "\x1b[?2026h\x1b[?7l" + string(frame) + "\x1b[?7h\x1b[?2026l"
	if out.String() != wantNoCursor {
		t.Fatalf("inactive frame wrapping mismatch:\ngot:  %q\nwant: %q", out.String(), wantNoCursor)
	}
}
