package tui

import (
	"strings"
	"testing"
)

func TestExtractAndHighlightSelectionSingleAndMultiLine(t *testing.T) {
	lines := []string{
		"\x1b[32mhello\x1b[0m world",
		"中文测试 ABC 123   ",
		"third line end",
	}

	// Single-line forward selection over ANSI-colored text ("llo wor").
	sel1 := textSel{active: true, startX: 2, startY: 0, endX: 8, endY: 0}
	if got := extractSelectedText(lines, sel1); got != "llo wor" {
		t.Fatalf("single line forward: got %q, want %q", got, "llo wor")
	}

	// Single-line reverse selection (right-to-left) over CJK wide chars: "中文" occupies cols 0..3.
	selCJK := textSel{active: true, startX: 3, startY: 1, endX: 0, endY: 1}
	if got := extractSelectedText(lines, selCJK); got != "中文" {
		t.Fatalf("CJK reverse selection: got %q, want %q", got, "中文")
	}

	// Multi-line selection from col 6 on line 0 to col 4 on line 2.
	selMulti := textSel{active: true, startX: 6, startY: 0, endX: 4, endY: 2}
	wantMulti := "world\n中文测试 ABC 123\nthird"
	if got := extractSelectedText(lines, selMulti); got != wantMulti {
		t.Fatalf("multi-line selection:\ngot:  %q\nwant: %q", got, wantMulti)
	}

	// Highlight injects selection background SGR without corrupting surrounding text.
	hl := applySelectionHighlight(lines, sel1)
	if !strings.Contains(hl[0], "\x1b[48;2;56;189;248;38;2;15;23;42mllo wor\x1b[0m") {
		t.Fatalf("expected highlighted span in line 0, got %q", hl[0])
	}
	if hl[2] != lines[2] {
		t.Fatalf("unselected line 2 should remain unchanged")
	}
}
