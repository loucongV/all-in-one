package tui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type dirEntryKind int

const (
	dirEntryCurrent dirEntryKind = iota
	dirEntryParent
	dirEntryChild
)

type dirEntry struct {
	kind    dirEntryKind
	name    string
	path    string
	isGit   bool
	isAdded bool
}

// dirPicker provides an interactive keyboard-driven directory browser inside the TUI modal.
type dirPicker struct {
	currentDir string
	showHidden bool
	entries    []dirEntry
	cursor     int
	scroll     int
	maxVisible int
	err        string
}

func newDirPicker(startDir string, addedPaths map[string]bool) dirPicker {
	if startDir == "" {
		startDir, _ = os.Getwd()
	}
	if startDir == "" {
		startDir, _ = os.UserHomeDir()
	}
	abs, err := filepath.Abs(startDir)
	if err == nil {
		startDir = abs
	}
	p := dirPicker{
		currentDir: startDir,
		maxVisible: 12,
	}
	p.reload(addedPaths)
	return p
}

func (p *dirPicker) reload(addedPaths map[string]bool) {
	p.err = ""
	p.entries = nil
	p.cursor = 0
	p.scroll = 0

	curGit := hasGitDir(p.currentDir)
	p.entries = append(p.entries, dirEntry{
		kind:    dirEntryCurrent,
		name:    ".",
		path:    p.currentDir,
		isGit:   curGit,
		isAdded: addedPaths[p.currentDir],
	})

	parent := filepath.Dir(p.currentDir)
	if parent != p.currentDir {
		p.entries = append(p.entries, dirEntry{
			kind: dirEntryParent,
			name: "..",
			path: parent,
		})
	}

	items, err := os.ReadDir(p.currentDir)
	if err != nil {
		p.err = err.Error()
		return
	}

	var dirs []dirEntry
	for _, item := range items {
		name := item.Name()
		if !p.showHidden && strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(p.currentDir, name)
		isDir := item.IsDir()
		if !isDir && item.Type()&os.ModeSymlink != 0 {
			if fi, statErr := os.Stat(full); statErr == nil && fi.IsDir() {
				isDir = true
			}
		}
		if !isDir {
			continue
		}
		dirs = append(dirs, dirEntry{
			kind:    dirEntryChild,
			name:    name,
			path:    full,
			isGit:   hasGitDir(full),
			isAdded: addedPaths[full],
		})
	}

	sort.Slice(dirs, func(i, j int) bool {
		return strings.ToLower(dirs[i].name) < strings.ToLower(dirs[j].name)
	})
	p.entries = append(p.entries, dirs...)
}

func (p *dirPicker) moveUp() {
	if p.cursor > 0 {
		p.cursor--
		if p.cursor < p.scroll {
			p.scroll = p.cursor
		}
	}
}

func (p *dirPicker) moveDown() {
	if p.cursor < len(p.entries)-1 {
		p.cursor++
		if p.cursor >= p.scroll+p.maxVisible {
			p.scroll = p.cursor - p.maxVisible + 1
		}
	}
}

func (p *dirPicker) goUp(addedPaths map[string]bool) {
	parent := filepath.Dir(p.currentDir)
	if parent == p.currentDir {
		return
	}
	prevName := filepath.Base(p.currentDir)
	p.currentDir = parent
	p.reload(addedPaths)
	// Restore cursor onto the folder we just came up from.
	for i, e := range p.entries {
		if e.kind == dirEntryChild && e.name == prevName {
			p.cursor = i
			if p.cursor >= p.maxVisible {
				p.scroll = p.cursor - p.maxVisible + 1
			}
			break
		}
	}
}

func (p *dirPicker) enterSelectedDir(addedPaths map[string]bool) {
	if len(p.entries) == 0 || p.cursor >= len(p.entries) {
		return
	}
	e := p.entries[p.cursor]
	switch e.kind {
	case dirEntryParent:
		p.goUp(addedPaths)
	case dirEntryChild:
		p.currentDir = e.path
		p.reload(addedPaths)
	}
}

func (p *dirPicker) goHome(addedPaths map[string]bool) {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		p.currentDir = home
		p.reload(addedPaths)
	}
}

func (p *dirPicker) toggleHidden(addedPaths map[string]bool) {
	p.showHidden = !p.showHidden
	p.reload(addedPaths)
}

func (p *dirPicker) selected() (dirEntry, bool) {
	if len(p.entries) == 0 || p.cursor >= len(p.entries) {
		return dirEntry{}, false
	}
	return p.entries[p.cursor], true
}

// createSubDir validates and creates a new subdirectory (supports nested paths like "a/b")
// under currentDir, optionally runs `git init`, reloads the listing, and selects the new folder.
func (p *dirPicker) createSubDir(relName string, initGit bool, addedPaths map[string]bool) (string, error) {
	name := strings.TrimSpace(relName)
	if name == "" {
		return "", errors.New("文件夹名称不能为空")
	}
	if strings.ContainsRune(name, 0) || filepath.IsAbs(name) {
		return "", errors.New("请输入相对当前目录的合法文件夹名称")
	}
	cleanRel := filepath.Clean(name)
	if cleanRel == "." || cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(os.PathSeparator)) {
		return "", errors.New("文件夹名称不能包含上级跳转 (..)")
	}
	target := filepath.Join(p.currentDir, cleanRel)
	if !strings.HasPrefix(target, filepath.Clean(p.currentDir)+string(os.PathSeparator)) {
		return "", errors.New("目标路径超出当前目录范围")
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return "", fmt.Errorf("创建文件夹失败: %w", err)
	}
	if initGit && !hasGitDir(target) {
		if gitBin, err := exec.LookPath("git"); err == nil {
			cmd := exec.Command(gitBin, "init")
			cmd.Dir = target
			if out, err := cmd.CombinedOutput(); err != nil {
				return target, fmt.Errorf("目录已创建，但 git init 失败: %s (%w)", strings.TrimSpace(string(out)), err)
			}
		}
	}

	// If nested path (e.g. a/b), navigate into parent of target so target is immediately visible & selected.
	targetParent := filepath.Dir(target)
	targetBase := filepath.Base(target)
	if strings.HasPrefix(targetBase, ".") {
		p.showHidden = true
	}
	p.currentDir = targetParent
	p.reload(addedPaths)
	for i, e := range p.entries {
		if e.kind == dirEntryChild && e.name == targetBase {
			p.cursor = i
			if p.cursor >= p.maxVisible {
				p.scroll = p.cursor - p.maxVisible + 1
			}
			break
		}
	}
	return target, nil
}

func (p *dirPicker) render() string {
	var b strings.Builder
	b.WriteString(mutedStyle.Render("当前浏览：") + headerTitleStyle.Render(p.currentDir) + "\n")
	if p.err != "" {
		b.WriteString(errorStyle.Render("无法读取目录: "+p.err) + "\n")
		return b.String()
	}

	b.WriteString(mutedStyle.Render(strings.Repeat("─", 58)) + "\n")

	end := min(len(p.entries), p.scroll+p.maxVisible)
	for i := p.scroll; i < end; i++ {
		e := p.entries[i]
		prefix := "  "
		if i == p.cursor {
			prefix = "▶ "
		}

		var label string
		switch e.kind {
		case dirEntryCurrent:
			label = fmt.Sprintf("✓ .  (选择当前目录: %s)", filepath.Base(e.path))
		case dirEntryParent:
			label = "↰ .. (返回上级目录)"
		case dirEntryChild:
			label = "📁 " + e.name + "/"
		}

		var tags []string
		if i == p.cursor {
			if e.isGit {
				tags = append(tags, "[git]")
			}
			if e.isAdded {
				tags = append(tags, "[已添加]")
			}
		} else {
			if e.isGit {
				tags = append(tags, okStyle.Render("[git]"))
			}
			if e.isAdded {
				tags = append(tags, mutedStyle.Render("[已添加]"))
			}
		}
		tagStr := ""
		if len(tags) > 0 {
			tagStr = "  " + strings.Join(tags, " ")
		}

		row := prefix + label + tagStr
		if i == p.cursor {
			row = selectedRowStyle.Width(58).Render(row)
		}
		b.WriteString(row + "\n")
	}

	if len(p.entries) > p.maxVisible {
		b.WriteString(mutedStyle.Render(fmt.Sprintf("  (%d/%d 项 · ↑/↓ 滚动)", p.cursor+1, len(p.entries))) + "\n")
	}
	return b.String()
}

func hasGitDir(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}
