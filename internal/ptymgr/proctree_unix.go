package ptymgr

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// collectDescendantPIDs returns all descendant PIDs of rootPID (children, grandchildren, ...)
// in bottom-up order so leaf workers (e.g. MCP servers, build subprocesses) can be signaled
// alongside the process group without leaking orphan processes.
func collectDescendantPIDs(rootPID int) []int {
	if rootPID <= 1 {
		return nil
	}
	visited := map[int]bool{rootPID: true}
	var ordered []int

	var walk func(pid int)
	walk = func(pid int) {
		for _, child := range directChildPIDs(pid) {
			if child <= 1 || visited[child] {
				continue
			}
			visited[child] = true
			walk(child)
			ordered = append(ordered, child)
		}
	}
	walk(rootPID)
	return ordered
}

func directChildPIDs(pid int) []int {
	if runtime.GOOS == "linux" {
		if kids := linuxChildPIDs(pid); len(kids) > 0 {
			return kids
		}
	}
	return pgrepChildPIDs(pid)
}

func linuxChildPIDs(pid int) []int {
	pattern := filepath.Join("/proc", strconv.Itoa(pid), "task", "*", "children")
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return nil
	}
	var kids []int
	seen := make(map[int]bool)
	for _, path := range matches {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, field := range strings.Fields(string(raw)) {
			c, err := strconv.Atoi(field)
			if err == nil && c > 1 && !seen[c] {
				seen[c] = true
				kids = append(kids, c)
			}
		}
	}
	return kids
}

func pgrepChildPIDs(pid int) []int {
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output()
	if err != nil {
		return nil
	}
	var kids []int
	for _, field := range strings.Fields(string(out)) {
		if c, err := strconv.Atoi(field); err == nil && c > 1 {
			kids = append(kids, c)
		}
	}
	return kids
}

func signalProcessTree(rootPID int, sig syscall.Signal) {
	if rootPID <= 1 {
		return
	}
	descendants := collectDescendantPIDs(rootPID)
	// Signal process group first, then individual descendants (including any that called setsid), then root.
	_ = syscall.Kill(-rootPID, sig)
	for _, cpid := range descendants {
		_ = syscall.Kill(-cpid, sig)
		_ = syscall.Kill(cpid, sig)
	}
	_ = syscall.Kill(rootPID, sig)
}
