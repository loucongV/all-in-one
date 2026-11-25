package tui

import (
	"bytes"
	"os"
	"strings"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// DetectColorProfile selects an optimal, reliable terminal color profile for local terminals,
// SSH clients (MobaXterm, PuTTY, OpenSSH), and terminal multiplexers (tmux, GNU screen).
//
// Why this matters:
//   - termenv defaults TERM=screen / TERM=tmux (without "-256color") to Ascii (zero color!),
//     causing all TUI colors to vanish inside default tmux sessions.
//   - termenv defaults TERM=xterm (MobaXterm's default over SSH) to 16-color ANSI, collapsing
//     dark slate backgrounds and sky-blue text into the same ANSI blue.
//   - Inside tmux without `terminal-overrides ",*:Tc"` / `RGB`, 24-bit TrueColor sequences
//     (\x1b[38;2;...m) are silently stripped by tmux, whereas 256-color (\x1b[38;5;...m)
//     renders reliably in every tmux configuration.
func DetectColorProfile(getenv func(string) string) (termenv.Profile, colorprofile.Profile) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if strings.TrimSpace(getenv("NO_COLOR")) != "" {
		return termenv.Ascii, colorprofile.Ascii
	}

	switch strings.ToLower(strings.TrimSpace(getenv("AIO_COLOR"))) {
	case "truecolor", "24bit", "rgb":
		return termenv.TrueColor, colorprofile.TrueColor
	case "256", "ansi256":
		return termenv.ANSI256, colorprofile.ANSI256
	case "16", "ansi":
		return termenv.ANSI, colorprofile.ANSI
	case "none", "ascii", "0", "off":
		return termenv.Ascii, colorprofile.Ascii
	}

	term := strings.ToLower(strings.TrimSpace(getenv("TERM")))
	colorTerm := strings.ToLower(strings.TrimSpace(getenv("COLORTERM")))
	termProg := strings.ToLower(strings.TrimSpace(getenv("TERM_PROGRAM")))
	inTmuxOrScreen := getenv("TMUX") != "" ||
		strings.HasPrefix(term, "tmux") ||
		strings.HasPrefix(term, "screen")

	if term == "dumb" {
		return termenv.Ascii, colorprofile.Ascii
	}

	// Inside tmux/screen: default to ANSI256 so colors never vanish on TERM=screen/tmux
	// and never get dropped by non-RGB tmux configs. Users with RGB-enabled tmux can still
	// opt in with AIO_COLOR=truecolor.
	if inTmuxOrScreen {
		return termenv.ANSI256, colorprofile.ANSI256
	}

	// Known TrueColor environments outside tmux.
	if colorTerm == "truecolor" || colorTerm == "24bit" || getenv("WT_SESSION") != "" {
		return termenv.TrueColor, colorprofile.TrueColor
	}
	switch term {
	case "alacritty", "contour", "rio", "wezterm", "xterm-ghostty", "xterm-kitty":
		return termenv.TrueColor, colorprofile.TrueColor
	}
	switch termProg {
	case "iterm.app", "wezterm", "vscode", "ghostty", "hyper", "rio", "warpterminal":
		return termenv.TrueColor, colorprofile.TrueColor
	}

	// Standard xterm / MobaXterm / SSH / 256color terminals: reliably support 256 colors.
	if strings.Contains(term, "256color") || term == "xterm" || term == "xterm-color" || strings.HasPrefix(term, "xterm-") || strings.HasPrefix(term, "rxvt") {
		return termenv.ANSI256, colorprofile.ANSI256
	}

	if strings.Contains(term, "color") || strings.Contains(term, "ansi") || term == "linux" {
		return termenv.ANSI, colorprofile.ANSI
	}

	return termenv.ANSI256, colorprofile.ANSI256
}

// ConfigureColorProfile applies the detected color profile to Lipgloss's default renderer
// and returns the matching colorprofile.Profile for downsampling embedded PTY ANSI frames.
func ConfigureColorProfile() colorprofile.Profile {
	tp, cp := DetectColorProfile(os.Getenv)
	lipgloss.SetColorProfile(tp)
	return cp
}

// convertPTYScreenForProfile downsamples TrueColor ANSI escape sequences emitted by the
// embedded VT emulator (Claude/Codex/Agy/tmux) to match the host terminal's color profile.
func convertPTYScreenForProfile(screen string, prof colorprofile.Profile) string {
	if screen == "" || prof == colorprofile.TrueColor || prof == colorprofile.Unknown {
		return screen
	}
	var buf bytes.Buffer
	buf.Grow(len(screen))
	w := colorprofile.Writer{Forward: &buf, Profile: prof}
	_, _ = w.WriteString(screen)
	return buf.String()
}
