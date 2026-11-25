package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// textSel tracks an in-pane mouse drag text selection in 0-based PTY viewport coordinates.
type textSel struct {
	dragging bool
	active   bool
	startX   int
	startY   int
	endX     int
	endY     int
}

func (s *textSel) clear() {
	*s = textSel{}
}

func (s textSel) normalized() (x0, y0, x1, y1 int) {
	if s.startY < s.endY || (s.startY == s.endY && s.startX <= s.endX) {
		return s.startX, s.startY, s.endX, s.endY
	}
	return s.endX, s.endY, s.startX, s.startY
}

// extractSelectedText extracts clean plain text from screenLines covered by sel,
// accurately slicing by terminal display cells (including CJK wide characters and ANSI styles).
func extractSelectedText(screenLines []string, sel textSel) string {
	if !sel.active || len(screenLines) == 0 {
		return ""
	}
	x0, y0, x1, y1 := sel.normalized()
	if y0 < 0 {
		y0 = 0
	}
	if y1 >= len(screenLines) {
		y1 = len(screenLines) - 1
	}
	if y0 > y1 {
		return ""
	}

	var out []string
	for y := y0; y <= y1; y++ {
		line := screenLines[y]
		w := ansi.StringWidth(line)
		var seg string
		switch {
		case y0 == y1:
			left := clampInt(x0, 0, w)
			right := clampInt(x1+1, left, w)
			seg = ansi.Strip(ansi.Cut(line, left, right))
			// Trim trailing padding spaces only if user dragged past the line's visible text.
			if x1+1 >= w {
				seg = strings.TrimRight(seg, " \r")
			}
		case y == y0:
			left := clampInt(x0, 0, w)
			seg = strings.TrimRight(ansi.Strip(ansi.Cut(line, left, w)), " \r")
		case y == y1:
			right := clampInt(x1+1, 0, w)
			seg = strings.TrimRight(ansi.Strip(ansi.Cut(line, 0, right)), " \r")
		default:
			seg = strings.TrimRight(ansi.Strip(line), " \r")
		}
		out = append(out, seg)
	}
	// Remove trailing completely blank lines when dragging below the last printed row.
	for len(out) > 1 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

// applySelectionHighlight paints a crisp high-contrast highlight over the selected display
// cell range on screenLines while preserving surrounding ANSI colors.
func applySelectionHighlight(screenLines []string, sel textSel) []string {
	if !sel.active || len(screenLines) == 0 {
		return screenLines
	}
	x0, y0, x1, y1 := sel.normalized()
	if y0 < 0 {
		y0 = 0
	}
	if y1 >= len(screenLines) {
		y1 = len(screenLines) - 1
	}
	if y0 > y1 {
		return screenLines
	}

	const selOpen = "\x1b[48;2;56;189;248;38;2;15;23;42m"
	const selClose = "\x1b[0m"

	res := make([]string, len(screenLines))
	copy(res, screenLines)

	for y := y0; y <= y1; y++ {
		line := res[y]
		w := ansi.StringWidth(line)
		var left, right int
		switch {
		case y0 == y1:
			left = clampInt(x0, 0, w)
			right = clampInt(x1+1, left, w)
		case y == y0:
			left = clampInt(x0, 0, w)
			right = w
		case y == y1:
			left = 0
			right = clampInt(x1+1, 0, w)
		default:
			left = 0
			right = w
		}
		if right <= left {
			continue
		}
		prefix := ansi.Cut(line, 0, left)
		mid := ansi.Strip(ansi.Cut(line, left, right))
		suffix := ansi.Cut(line, right, w)
		res[y] = prefix + selOpen + mid + selClose + suffix
	}
	return res
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
