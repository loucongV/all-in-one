package quota

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

// ProbeAgentUsage runs a short-lived, hidden PTY session in a dedicated empty
// probe directory (<aioHome>/probe), invokes the CLI's built-in /usage command,
// captures the rendered VT screen text, and terminates cleanly.
func ProbeAgentUsage(ctx context.Context, agent, aioHome string) (string, error) {
	var bin string
	var args []string

	switch agent {
	case "agy":
		p, err := exec.LookPath("agy")
		if err != nil {
			return "", err
		}
		bin = p
	case "claude":
		p, err := exec.LookPath("claude")
		if err != nil {
			return "", err
		}
		bin = p
	case "cursor":
		if p, err := exec.LookPath("cursor-agent"); err == nil {
			bin = p
		} else if p, err := exec.LookPath("agent"); err == nil {
			bin = p
		} else {
			return "", err
		}
		args = []string{"--trust"}
	default:
		return "", fmt.Errorf("silent probe not supported for agent %s", agent)
	}

	probeDir := filepath.Join(aioHome, "probe")
	if aioHome == "" {
		probeDir = filepath.Join(os.TempDir(), "aio-probe")
	}
	if err := os.MkdirAll(probeDir, 0o755); err != nil {
		return "", err
	}

	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	cmd := exec.Command(bin, args...)
	cmd.Dir = probeDir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 40})
	if err != nil {
		return "", err
	}

	emu := vt.NewSafeEmulator(120, 40)
	emu.Focus()

	doneRead := make(chan struct{})
	go func() {
		defer close(doneRead)
		buf := make([]byte, 16*1024)
		for {
			n, rErr := ptmx.Read(buf)
			if n > 0 {
				_, _ = emu.Write(buf[:n])
			}
			if rErr != nil {
				return
			}
		}
	}()

	// Answer terminal queries (DSR/CPR/DA) so Ink/BubbleTea/Ratatui CLIs boot immediately.
	go func() {
		buf := make([]byte, 512)
		for {
			n, rErr := emu.Read(buf)
			if n > 0 {
				_, _ = ptmx.Write(buf[:n])
			}
			if rErr != nil {
				return
			}
		}
	}()

	cleanup := func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			_ = cmd.Process.Signal(syscall.SIGTERM)
		}
		waitDone := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(waitDone)
		}()
		select {
		case <-waitDone:
		case <-time.After(800 * time.Millisecond):
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				_ = cmd.Process.Kill()
			}
			<-waitDone
		}
		_ = ptmx.Close()
		if closer, ok := emu.InputPipe().(io.Closer); ok {
			_ = closer.Close()
		}
	}
	defer cleanup()

	var (
		trustedSent bool
		dismissSent bool
		usageSent   bool
		secondEnter bool
		usageSentAt time.Time
		lastScreen  string
	)

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	bootStart := time.Now()
	for {
		select {
		case <-probeCtx.Done():
			return emu.String(), nil
		case <-doneRead:
			return emu.String(), nil
		case <-ticker.C:
			screen := emu.String()
			lower := strings.ToLower(screen)
			lastScreen = screen

			// 1. Handle first-time trust dialog inside our dedicated empty ~/.aio/probe folder.
			if !trustedSent && strings.Contains(lower, "trust this folder") {
				trustedSent = true
				if agent == "claude" && strings.Contains(lower, "no, exit") {
					// Claude defaults cursor to "No, exit"; move down to "Yes, I trust this folder".
					_, _ = ptmx.Write([]byte("\x1b[B"))
					time.Sleep(150 * time.Millisecond)
					_, _ = ptmx.Write([]byte("\r"))
				} else {
					_, _ = ptmx.Write([]byte("\r"))
				}
				continue
			}

			// Dismiss transient onboarding banner (e.g. agy "esc to dismiss").
			if !dismissSent && strings.Contains(lower, "esc to dismiss") {
				dismissSent = true
				_, _ = ptmx.Write([]byte("\x1b"))
				continue
			}

			// 2. Once booted and ready, send /usage.
			ready := time.Since(bootStart) > 2*time.Second &&
				(strings.Contains(screen, "❯") || strings.Contains(screen, ">") || strings.Contains(screen, "→") || strings.Contains(lower, "for shortcuts") || strings.Contains(lower, "plan, search")) &&
				!strings.Contains(lower, "trust this folder")
			if !usageSent && ready {
				usageSent = true
				usageSentAt = time.Now()
				_, _ = ptmx.Write([]byte("/usage"))
				time.Sleep(350 * time.Millisecond)
				_, _ = ptmx.Write([]byte("\r"))
				continue
			}

			if usageSent {
				// Check if /usage output is already parseable!
				if _, ok := ParseUsageScreen(agent, screen); ok {
					// Wait one short tick to let all lines finish rendering, then return.
					time.Sleep(350 * time.Millisecond)
					return emu.String(), nil
				}
				// Some TUIs select the slash command from autocomplete on first Enter and run on second Enter.
				if !secondEnter && time.Since(usageSentAt) > 1500*time.Millisecond {
					secondEnter = true
					_, _ = ptmx.Write([]byte("\r"))
					continue
				}
				if time.Since(usageSentAt) > 7*time.Second {
					return lastScreen, nil
				}
			}
		}
	}
}
