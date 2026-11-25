package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"github.com/muesli/termenv"
)

func TestDetectColorProfileForTmuxMobaXtermAndTrueColor(t *testing.T) {
	mkEnv := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}

	// 1. Default tmux (TERM=screen or TERM=tmux without -256color) MUST NOT downgrade to Ascii!
	tp, cp := DetectColorProfile(mkEnv(map[string]string{"TERM": "screen", "TMUX": "/tmp/tmux-1000/default,123,0"}))
	if tp != termenv.ANSI256 || cp != colorprofile.ANSI256 {
		t.Fatalf("expected ANSI256 in tmux/screen, got termenv=%v cp=%v", tp, cp)
	}

	// 2. tmux even with inherited COLORTERM=truecolor defaults safely to ANSI256 (avoids RGB loss in non-Tc tmux).
	tp, cp = DetectColorProfile(mkEnv(map[string]string{"TERM": "tmux-256color", "TMUX": "/tmp/tmux-1000/default,1,0", "COLORTERM": "truecolor"}))
	if tp != termenv.ANSI256 || cp != colorprofile.ANSI256 {
		t.Fatalf("expected safe ANSI256 in tmux, got termenv=%v cp=%v", tp, cp)
	}

	// 3. Explicit AIO_COLOR=truecolor overrides tmux default when user has RGB enabled.
	tp, cp = DetectColorProfile(mkEnv(map[string]string{"TERM": "tmux-256color", "TMUX": "1", "AIO_COLOR": "truecolor"}))
	if tp != termenv.TrueColor || cp != colorprofile.TrueColor {
		t.Fatalf("expected AIO_COLOR=truecolor override, got termenv=%v cp=%v", tp, cp)
	}

	// 4. MobaXterm SSH default TERM=xterm MUST get ANSI256 instead of 16-color ANSI.
	tp, cp = DetectColorProfile(mkEnv(map[string]string{"TERM": "xterm"}))
	if tp != termenv.ANSI256 || cp != colorprofile.ANSI256 {
		t.Fatalf("expected ANSI256 for MobaXterm TERM=xterm, got termenv=%v cp=%v", tp, cp)
	}

	// 5. Modern local terminal with COLORTERM=truecolor outside tmux gets TrueColor.
	tp, cp = DetectColorProfile(mkEnv(map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}))
	if tp != termenv.TrueColor || cp != colorprofile.TrueColor {
		t.Fatalf("expected TrueColor for COLORTERM=truecolor, got termenv=%v cp=%v", tp, cp)
	}

	// 6. Downsampling TrueColor PTY output for ANSI256 converts 38;2;R;G;B to 38;5;N.
	tc := "\x1b[38;2;56;189;248mhello\x1b[0m"
	down := convertPTYScreenForProfile(tc, colorprofile.ANSI256)
	if strings.Contains(down, "38;2;") || !strings.Contains(down, "38;5;") {
		t.Fatalf("expected TrueColor SGR to downsample to 256-color, got %q", down)
	}
}
