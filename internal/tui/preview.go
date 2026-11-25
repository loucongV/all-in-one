package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const maxPreviewReadBytes = 512 * 1024

var (
	fragLineRangeRe  = regexp.MustCompile(`(?i)^L?(\d+)(?:-L?(\d+))?$`)
	pathLineSuffixRe = regexp.MustCompile(`^(.*):(\d+)(?::\d+)?$`)
)

// parseFileLink parses a file:// hyperlink (including optional #L10-L20, #L10, or :10 anchors)
// into a clean local filesystem path and 1-based line range.
func parseFileLink(rawURL string) (filePath string, startLine, endLine int, ok bool) {
	u := strings.TrimSpace(rawURL)
	if !strings.HasPrefix(strings.ToLower(u), "file://") {
		return "", 0, 0, false
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return "", 0, 0, false
	}
	p := parsed.Path
	if unesc, err := url.PathUnescape(p); err == nil {
		p = unesc
	}
	if p == "" {
		return "", 0, 0, false
	}

	// Parse fragment like #L3-L6, #L42, or #42.
	if frag := strings.TrimSpace(parsed.Fragment); frag != "" {
		if m := fragLineRangeRe.FindStringSubmatch(frag); len(m) >= 2 {
			startLine, _ = strconv.Atoi(m[1])
			if len(m) >= 3 && m[2] != "" {
				endLine, _ = strconv.Atoi(m[2])
			} else {
				endLine = startLine
			}
		}
	}

	// Fallback: handle path suffix like /path/to/file.go:42 or /path/to/file.go:42:10
	// if the raw path doesn't exist on disk.
	if startLine == 0 {
		if _, statErr := os.Stat(p); statErr != nil {
			if m := pathLineSuffixRe.FindStringSubmatch(p); len(m) == 3 {
				if _, err2 := os.Stat(m[1]); err2 == nil {
					p = m[1]
					startLine, _ = strconv.Atoi(m[2])
					endLine = startLine
				}
			}
		}
	}

	if endLine > 0 && endLine < startLine {
		startLine, endLine = endLine, startLine
	}
	return filepath.Clean(p), startLine, endLine, true
}

type filePreviewData struct {
	Path       string
	StartLine  int
	EndLine    int
	TotalLines int
	IsDir      bool
	Truncated  bool
	Content    string
}

func loadFilePreview(filePath string, startLine, endLine, contentWidth int) (filePreviewData, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return filePreviewData{}, fmt.Errorf("无法读取本地文件: %w", err)
	}
	if contentWidth < 30 {
		contentWidth = 76
	}

	if info.IsDir() {
		entries, err := os.ReadDir(filePath)
		if err != nil {
			return filePreviewData{}, err
		}
		sort.Slice(entries, func(i, j int) bool {
			di, dj := entries[i].IsDir(), entries[j].IsDir()
			if di != dj {
				return di
			}
			return entries[i].Name() < entries[j].Name()
		})
		var b strings.Builder
		for _, e := range entries {
			if e.IsDir() {
				b.WriteString("  " + keyHintStyle.Render("▸ "+e.Name()+"/") + "\n")
			} else {
				sz := ""
				if fi, err := e.Info(); err == nil {
					sz = mutedStyle.Render(fmt.Sprintf("  (%s)", formatByteSize(fi.Size())))
				}
				b.WriteString("    " + e.Name() + sz + "\n")
			}
		}
		if len(entries) == 0 {
			b.WriteString(mutedStyle.Render("  （空目录）"))
		}
		return filePreviewData{
			Path:       filePath,
			IsDir:      true,
			TotalLines: len(entries),
			Content:    strings.TrimRight(b.String(), "\n"),
		}, nil
	}

	f, err := os.Open(filePath)
	if err != nil {
		return filePreviewData{}, err
	}
	defer f.Close()

	raw, err := io.ReadAll(io.LimitReader(f, maxPreviewReadBytes+1))
	if err != nil {
		return filePreviewData{}, err
	}
	truncated := len(raw) > maxPreviewReadBytes
	if truncated {
		raw = raw[:maxPreviewReadBytes]
	}

	// Detect binary files by checking NUL bytes in the first 4KB.
	checkLen := len(raw)
	if checkLen > 4096 {
		checkLen = 4096
	}
	if bytes.IndexByte(raw[:checkLen], 0) >= 0 {
		msg := fmt.Sprintf("  二进制文件（%s），不支持文本预览。\n\n  按 o 可调用系统默认程序打开，按 c 复制文件路径。", formatByteSize(info.Size()))
		return filePreviewData{
			Path:       filePath,
			TotalLines: 1,
			Content:    mutedStyle.Render(msg),
		}, nil
	}

	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")

	numW := len(strconv.Itoa(len(lines)))
	if numW < 3 {
		numW = 3
	}
	codeW := max(20, contentWidth-numW-5)

	hlLineStyle := lipgloss.NewStyle().Background(lipgloss.Color("#1E3A5F")).Foreground(lipgloss.Color("#F8FAFC"))
	hlNumStyle := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	numStyle := lipgloss.NewStyle().Foreground(colorDim)

	var out strings.Builder
	for i, rawLine := range lines {
		ln := i + 1
		expanded := strings.ReplaceAll(rawLine, "\t", "    ")
		if ansi.StringWidth(expanded) > codeW {
			expanded = ansi.Truncate(expanded, codeW, "…")
		}
		inRange := startLine > 0 && ln >= startLine && ln <= max(startLine, endLine)
		if inRange {
			prefix := hlNumStyle.Render(fmt.Sprintf("▸%*d ", numW, ln)) + treeGuideStyle.Render("│ ")
			out.WriteString(prefix + hlLineStyle.Width(codeW).Render(expanded) + "\n")
		} else {
			prefix := numStyle.Render(fmt.Sprintf(" %*d ", numW, ln)) + treeGuideStyle.Render("│ ")
			out.WriteString(prefix + expanded + "\n")
		}
	}
	if truncated {
		out.WriteString("\n" + warnStyle.Render("  … 文件超过 512KB，仅预览前 512KB（按 o 使用外部编辑器打开完整文件）"))
	}

	return filePreviewData{
		Path:       filePath,
		StartLine:  startLine,
		EndLine:    endLine,
		TotalLines: len(lines),
		Truncated:  truncated,
		Content:    strings.TrimRight(out.String(), "\n"),
	}, nil
}

func formatByteSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

// openLocalFileExternal opens a local file or directory in VS Code / system default app across WSL2, macOS, and Linux.
func openLocalFileExternal(filePath string, line int) error {
	var candidates [][]string

	// Prefer VS Code CLI with exact line jump when available.
	if _, err := exec.LookPath("code"); err == nil {
		target := filePath
		if line > 0 {
			target = fmt.Sprintf("%s:%d", filePath, line)
			candidates = append(candidates, []string{"code", "--goto", target})
		} else {
			candidates = append(candidates, []string{"code", target})
		}
	}

	if os.Getenv("WSL_DISTRO_NAME") != "" || os.Getenv("WSL_INTEROP") != "" {
		winPath := filePath
		if wslpathBin, err := exec.LookPath("wslpath"); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			out, err := exec.CommandContext(ctx, wslpathBin, "-w", filePath).Output()
			cancel()
			if err == nil && len(bytes.TrimSpace(out)) > 0 {
				winPath = string(bytes.TrimSpace(out))
			}
		}
		candidates = append(candidates,
			[]string{"rundll32.exe", "url.dll,FileProtocolHandler", winPath},
			[]string{"explorer.exe", winPath},
		)
	} else if runtime.GOOS == "darwin" {
		candidates = append(candidates, []string{"open", filePath})
	} else {
		candidates = append(candidates,
			[]string{"xdg-open", filePath},
			[]string{"gio", "open", filePath},
		)
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
	return errors.New("未找到可用的本地文件打开程序")
}
