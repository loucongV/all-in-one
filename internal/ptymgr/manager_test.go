package ptymgr

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/loucongV/all-in-one/internal/adapter"
)

func TestSnapshotScreenDrawsVisibleCursor(t *testing.T) {
	mgr := NewManager(nil)
	defer mgr.StopAll()

	sess, err := mgr.Start("run-cursor-test", adapter.CommandSpec{
		Binary: "sh",
		Args:   []string{"-c", `printf "\033[?25h> Ask"; sleep 2`},
		Dir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		frame := sess.SnapshotScreen()
		if strings.Contains(frame.ANSI, "> Ask") {
			if frame.Cursor.Hidden {
				t.Fatalf("expected cursor to be visible")
			}
			// High-contrast cursor sets bg=226;232;240 and fg=15;23;42.
			if !strings.Contains(frame.ANSI, "48;2;226;232;240") {
				t.Fatalf("expected rendered screen to contain high-contrast cursor background, got: %q", frame.ANSI)
			}
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for PTY output")
}

func TestScrollbackAndPaste(t *testing.T) {
	mgr := NewManager(nil)
	defer mgr.StopAll()

	// Emit 60 lines so lines 1..20 scroll into the VT scrollback buffer (default height 36).
	sess, err := mgr.Start("run-scroll-test", adapter.CommandSpec{
		Binary: "sh",
		Args:   []string{"-c", `i=1; while [ $i -le 60 ]; do echo "LOGLINE-$i"; i=$((i+1)); done; read x; echo "PASTED:$x"; sleep 2`},
		Dir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		frame := sess.SnapshotScreen()
		if strings.Contains(frame.ANSI, "LOGLINE-60") && frame.ScrollMax > 0 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}

	live := sess.SnapshotScreen()
	if live.ScrollMax == 0 {
		t.Fatalf("expected non-empty scrollback buffer")
	}

	// Scroll up into scrollback and verify early log line becomes visible.
	sess.Scroll(30, 0, 0)
	scrolled := sess.SnapshotScreen()
	if scrolled.ScrollOffset == 0 || !strings.Contains(scrolled.ANSI, "LOGLINE-1") {
		t.Fatalf("expected scrolled screen to show LOGLINE-1, offset=%d", scrolled.ScrollOffset)
	}

	// Paste snaps scroll back to live bottom and sends text cleanly.
	if err := sess.Paste("hello-paste\r"); err != nil {
		t.Fatalf("Paste failed: %v", err)
	}
	if sess.SnapshotScreen().ScrollOffset != 0 {
		t.Fatalf("expected Paste to reset scrollOffset to 0")
	}
}

func TestSnapshotScreenExtractsOSC8AndPlainLinks(t *testing.T) {
	mgr := NewManager(nil)
	defer mgr.StopAll()

	// Emit both an OSC 8 markdown hyperlink over CJK text ("商品页") and a plain https URL.
	sess, err := mgr.Start("run-link-test", adapter.CommandSpec{
		Binary: "sh",
		Args:   []string{"-c", `printf '查看 \033]8;;https://mockupmaison.com/item?id=1&a=2\033\\商品页\033]8;;\033\\\n访问 https://example.com/docs/guide 了解\n'; sleep 2`},
		Dir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		frame := sess.SnapshotScreen()
		if len(frame.Links) >= 2 {
			foundOSC8 := false
			foundPlain := false
			for _, l := range frame.Links {
				if l.Row == 0 && l.URL == "https://mockupmaison.com/item?id=1&a=2" && l.StartX == 5 && l.EndX == 10 {
					foundOSC8 = true
				}
				if l.Row == 1 && l.URL == "https://example.com/docs/guide" {
					foundPlain = true
				}
			}
			if !foundOSC8 || !foundPlain {
				t.Fatalf("unexpected extracted links: %+v", frame.Links)
			}
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for OSC 8 and plain URL links")
}

func TestScrollbackPreservesWideCJKCharacters(t *testing.T) {
	mgr := NewManager(nil)
	defer mgr.StopAll()

	// Emit 50 lines of multi-character Chinese text so early lines enter VT scrollback.
	sess, err := mgr.Start("run-cjk-scroll-test", adapter.CommandSpec{
		Binary: "sh",
		Args:   []string{"-c", `i=1; while [ $i -le 50 ]; do echo "第${i}行 报错原因：启动失败的直接原因是缺少配置"; i=$((i+1)); done; sleep 2`},
		Dir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		frame := sess.SnapshotScreen()
		if frame.ScrollMax >= 10 && strings.Contains(frame.ANSI, "第50行") {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}

	sess.Scroll(20, 0, 0)
	scrolled := sess.SnapshotScreen()
	if scrolled.ScrollOffset == 0 {
		t.Fatalf("expected non-zero scrollOffset")
	}
	// Every Chinese character must remain intact (no leading wide char erased by its trailing cell!).
	if !strings.Contains(scrolled.ANSI, "报错原因：启动失败的直接原因是缺少配置") {
		t.Fatalf("scrollback corrupted wide CJK text! got:\n%s", scrolled.ANSI)
	}
}

func TestAltScreenScrollFallbackAndMouseForwarding(t *testing.T) {
	mgr := NewManager(nil)
	defer mgr.StopAll()

	// Child enters alt screen WITHOUT mouse tracking and echoes received bytes in hex via od.
	sess, err := mgr.Start("run-alt-fallback", adapter.CommandSpec{
		Binary: "sh",
		Args:   []string{"-c", `stty raw -echo; printf "\033[?1049hREADY"; dd bs=1 count=3 2>/dev/null | od -An -tx1; sleep 1`},
		Dir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(sess.SnapshotScreen().ANSI, "READY") {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	sess.Scroll(1, 0, 0) // should send ESC [ A (1b 5b 41)

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(sess.SnapshotScreen().ANSI, "1b 5b 41") {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("expected Up arrow fallback (1b 5b 41) in alt-screen without mouse mode, got:\n%s", sess.SnapshotScreen().ANSI)
}

func TestClassifyTerminalTitleCursorStates(t *testing.T) {
	cases := []struct {
		title  string
		status string
		detail string
		ok     bool
	}{
		{"Cursor Agent - ✅ Ready", "done", "title:ready", true},
		{"Hello Chat - ✅ Ready", "done", "title:ready", true},
		{"Hello Chat - Ready", "done", "title:ready", true},
		{"Hello Chat - ⏳ Working ···", "running", "title:working", true},
		{"Hello Chat - ⏳ Working ..·", "running", "title:working", true},
		{"Hello Chat - 🧭 Planning", "running", "title:planning", true},
		{"Hello Chat - ⌨️ Running shell command", "running", "title:shell_running", true},
		{"Hello Chat - ❓ Waiting for you", "waiting_input", "title:waiting_user", true},
		{"Hello Chat - 🔐 Waiting for confirmation", "waiting_input", "title:waiting_confirmation", true},
		{"Hello Chat - 📝 Reviewing changes", "waiting_input", "title:reviewing", true},
		// Regular shell/project titles must NOT falsely trigger status changes.
		{"liqin@ubuntu:~/project/working-dir", "", "", false},
		{"bash", "", "", false},
	}

	for _, tc := range cases {
		st, detail, ok := classifyTerminalTitle(tc.title)
		if ok != tc.ok || string(st) != tc.status || detail != tc.detail {
			t.Fatalf("classifyTerminalTitle(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.title, st, detail, ok, tc.status, tc.detail, tc.ok)
		}
	}
}

func TestPasteWrapsBracketedPasteAtomicallyWhenEnabled(t *testing.T) {
	mgr := NewManager(nil)
	defer mgr.StopAll()

	// Enable ?2004h bracketed paste mode, then read 23 bytes of raw stdin and print hex via od.
	// Expected bytes for "a\rb" wrapped in ESC[200~ ... ESC[201~: 6 + 3 + 6 = 15 bytes.
	sess, err := mgr.Start("run-bp-paste", adapter.CommandSpec{
		Binary: "sh",
		Args:   []string{"-c", `stty raw -echo; printf "\033[?2004hREADY"; dd bs=1 count=15 2>/dev/null | od -An -tx1; sleep 1`},
		Dir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(sess.SnapshotScreen().ANSI, "READY") {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	// Paste CRLF text; must normalize \r\n -> \r and wrap in \x1b[200~ ... \x1b[201~.
	if err := sess.Paste("a\r\nb"); err != nil {
		t.Fatalf("Paste failed: %v", err)
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		screen := sess.SnapshotScreen().ANSI
		// 1b 5b 32 30 30 7e = \x1b[200~, 61 0d 62 = "a\rb", 1b 5b 32 30 31 7e = \x1b[201~
		if strings.Contains(screen, "1b 5b 32 30 30 7e") && strings.Contains(screen, "61 0d 62") && strings.Contains(screen, "1b 5b 32 30 31 7e") {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("expected atomic bracketed paste bytes in output, got:\n%s", sess.SnapshotScreen().ANSI)
}

func TestMergeEnvUnsetAndRemoveReleasesSession(t *testing.T) {
	base := []string{"PATH=/usr/bin", "HTTP_PROXY=http://127.0.0.1:7897", "https_proxy=http://127.0.0.1:7897"}
	merged := mergeEnv(base, []string{"-HTTP_PROXY", "-https_proxy", "FOO=bar"})
	joined := strings.Join(merged, "\n")
	if strings.Contains(joined, "HTTP_PROXY") || strings.Contains(joined, "https_proxy") {
		t.Fatalf("expected unset proxy keys to be removed, got:\n%s", joined)
	}
	if !strings.Contains(joined, "FOO=bar") || !strings.Contains(joined, "PATH=/usr/bin") {
		t.Fatalf("expected preserved and added env vars, got:\n%s", joined)
	}

	mgr := NewManager(nil)
	defer mgr.StopAll()
	if _, err := mgr.Start("run-remove-tree", adapter.CommandSpec{
		Binary: "sh",
		Args:   []string{"-c", "sleep 30 & sleep 30"},
		Dir:    t.TempDir(),
	}); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if err := mgr.Remove("run-remove-tree"); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}
	if _, exists := mgr.Get("run-remove-tree"); exists {
		t.Fatalf("expected Remove to delete session from manager map")
	}
}

func TestSnapshotFullScreenIncludesScrollbackAndLiveRows(t *testing.T) {
	mgr := NewManager(nil)
	defer mgr.StopAll()

	sess, err := mgr.Start("full-sb", adapter.CommandSpec{
		Binary: "sh",
		Args:   []string{"-c", "sleep 0.15; for i in $(seq 1 60); do echo LINE_$i; done; sleep 2"},
		Dir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	_ = sess.Resize(80, 12)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		full := ansi.Strip(sess.SnapshotFullScreen(200).ANSI)
		if strings.Contains(full, "LINE_1") && strings.Contains(full, "LINE_60") {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("expected SnapshotFullScreen to contain both early LINE_1 and latest LINE_60, got:\n%s",
		ansi.Strip(sess.SnapshotFullScreen(200).ANSI))
}
