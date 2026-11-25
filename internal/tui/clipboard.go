package tui

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/atotto/clipboard"
)

var ansiCleanRe = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)

func stripANSI(s string) string {
	clean := ansiCleanRe.ReplaceAllString(s, "")
	lines := strings.Split(clean, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \r")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// isRemoteSSHSession reports whether aio is running inside a remote SSH shell
// where local desktop clipboard tools (wl-paste/xclip/pbpaste) are unavailable.
func isRemoteSSHSession() bool {
	return os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" || os.Getenv("SSH_CLIENT") != ""
}

// copyToClipboard writes text to:
//  1. OSC 52 (works in WezTerm, iTerm2, Kitty, Alacritty, VS Code, tmux, and over SSH)
//  2. Wayland wl-copy when WAYLAND_DISPLAY is active (atotto/clipboard only supports xclip/xsel)
//  3. X11 / macOS / Windows native clipboard via atotto/clipboard
func copyToClipboard(text string) error {
	if text == "" {
		return nil
	}
	// 1. Always emit OSC 52 to stdout for terminal emulator / remote SSH clipboard integration.
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	if os.Getenv("TMUX") != "" {
		_, _ = os.Stdout.WriteString("\x1bPtmux;\x1b\x1b]52;c;" + encoded + "\x07\x1b\\")
	} else {
		_, _ = os.Stdout.WriteString("\x1b]52;c;" + encoded + "\x07")
	}

	// 2. Wayland native wl-copy (must leave Stdout/Stderr nil so background fork never hangs).
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		if bin, err := exec.LookPath("wl-copy"); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin)
			cmd.Stdin = strings.NewReader(text)
			cmd.Stdout = nil
			cmd.Stderr = nil
			if err := cmd.Run(); err == nil {
				return nil
			}
		}
	}

	// 3. On headless remote SSH Linux without a display server, OSC 52 already handled copy.
	if runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return nil
	}

	// 4. Fallback to xclip/xsel/pbcopy/Windows clipboard.
	return clipboard.WriteAll(text)
}

func readFromClipboard() (string, error) {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		if bin, err := exec.LookPath("wl-paste"); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, "--no-newline")
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = nil
			if err := cmd.Run(); err == nil {
				return out.String(), nil
			}
		}
	}
	if runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return "", errors.New("ssh/headless environment has no server clipboard")
	}
	return clipboard.ReadAll()
}

// openURLInBrowser opens an http/https URL in the host default browser across WSL2, Linux, and macOS.
func openURLInBrowser(rawURL string) error {
	u := strings.TrimSpace(rawURL)
	parsed, err := url.Parse(u)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("invalid http/https url")
	}

	var candidates [][]string
	if os.Getenv("WSL_DISTRO_NAME") != "" || os.Getenv("WSL_INTEROP") != "" {
		// WSL2: open directly in Windows default browser without cmd.exe shell-escaping issues.
		candidates = [][]string{
			{"wslview", u},
			{"rundll32.exe", "url.dll,FileProtocolHandler", u},
			{"explorer.exe", u},
		}
	} else if runtime.GOOS == "darwin" {
		candidates = [][]string{{"open", u}}
	} else {
		candidates = [][]string{
			{"xdg-open", u},
			{"gio", "open", u},
			{"sensible-browser", u},
		}
	}

	for _, args := range candidates {
		bin, err := exec.LookPath(args[0])
		if err != nil {
			continue
		}
		cmd := exec.Command(bin, args[1:]...)
		cmd.Stdin = nil
		cmd.Stdout = nil
		cmd.Stderr = nil
		if err := cmd.Start(); err == nil {
			go func() { _ = cmd.Wait() }()
			return nil
		}
	}
	return errors.New("未找到可用的浏览器打开命令")
}
