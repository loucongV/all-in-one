package daemon

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// validSessionIDRe enforces safe, path-traversal-free session IDs before touching disk.
var validSessionIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{5,127}$`)

// ValidateSessionID verifies a session identifier contains no path separators or traversal segments.
func ValidateSessionID(sessionID string) error {
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return errors.New("empty session id")
	}
	if strings.Contains(sid, "..") || !validSessionIDRe.MatchString(sid) {
		return fmt.Errorf("unsafe session id: %q", sid)
	}
	return nil
}

// CleanCLISession removes on-disk conversation history files for a deleted run's bound CLI session.
// It validates sessionID strictly before performing any filesystem operation and returns nil
// if no matching on-disk session file exists.
func CleanCLISession(agent, workDir, sessionID, userHome string) error {
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return nil
	}
	if err := ValidateSessionID(sid); err != nil {
		return err
	}
	if userHome == "" {
		var err error
		userHome, err = os.UserHomeDir()
		if err != nil || userHome == "" {
			return err
		}
	}

	switch agent {
	case "claude":
		return cleanClaudeSession(userHome, workDir, sid)
	case "codex":
		return cleanCodexSession(userHome, sid)
	case "cursor":
		return cleanCursorSession(userHome, workDir, sid)
	case "agy":
		return cleanAgySession(userHome, sid)
	case "kimi":
		return cleanKimiSession(userHome, sid)
	case "opencode":
		return cleanOpenCodeSession(userHome, sid)
	default:
		return nil
	}
}

func cleanClaudeSession(userHome, workDir, sid string) error {
	projRoot := filepath.Join(userHome, ".claude", "projects")
	// Check encoded workDir first, then scan project directories so moved/worktree sessions are caught.
	var candidates []string
	if abs := cleanAbs(workDir); abs != "" {
		enc := strings.ReplaceAll(abs, "/", "-")
		candidates = append(candidates, filepath.Join(projRoot, enc))
	}
	if entries, err := os.ReadDir(projRoot); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				candidates = append(candidates, filepath.Join(projRoot, e.Name()))
			}
		}
	}
	seen := make(map[string]bool)
	for _, dir := range candidates {
		if seen[dir] {
			continue
		}
		seen[dir] = true
		jsonl := filepath.Join(dir, sid+".jsonl")
		if _, err := os.Stat(jsonl); err == nil {
			_ = os.Remove(jsonl)
		}
		sub := filepath.Join(dir, sid)
		if fi, err := os.Stat(sub); err == nil && fi.IsDir() {
			_ = os.RemoveAll(sub)
		}
	}
	return nil
}

func cleanCodexSession(userHome, sid string) error {
	root := filepath.Join(userHome, ".codex", "sessions")
	if _, err := os.Stat(root); err != nil {
		return nil
	}
	return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl") && strings.Contains(name, sid) {
			_ = os.Remove(p)
		}
		return nil
	})
}

func cleanCursorSession(userHome, workDir, sid string) error {
	chatsRoot := filepath.Join(userHome, ".cursor", "chats")
	if abs := cleanAbs(workDir); abs != "" {
		sum := md5.Sum([]byte(abs))
		target := filepath.Join(chatsRoot, hex.EncodeToString(sum[:]), sid)
		if fi, err := os.Stat(target); err == nil && fi.IsDir() {
			return os.RemoveAll(target)
		}
	}
	// Fallback: search workspace hash subdirectories for matching chat UUID directory.
	wsDirs, err := os.ReadDir(chatsRoot)
	if err != nil {
		return nil
	}
	for _, ws := range wsDirs {
		if !ws.IsDir() {
			continue
		}
		target := filepath.Join(chatsRoot, ws.Name(), sid)
		if fi, err := os.Stat(target); err == nil && fi.IsDir() {
			_ = os.RemoveAll(target)
		}
	}
	return nil
}

func cleanAgySession(userHome, sid string) error {
	base := filepath.Join(userHome, ".gemini", "antigravity-cli")
	brainDir := filepath.Join(base, "brain", sid)
	if fi, err := os.Stat(brainDir); err == nil && fi.IsDir() {
		_ = os.RemoveAll(brainDir)
	}
	dbPath := filepath.Join(base, "conversation_summaries.db")
	if _, err := os.Stat(dbPath); err == nil {
		if pyBin, err := exec.LookPath("python3"); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			script := `import sqlite3, sys
con = sqlite3.connect(sys.argv[1], timeout=1.5)
con.execute("UPDATE conversation_summaries SET killed=1 WHERE conversation_id=?", (sys.argv[2],))
con.commit()
con.close()`
			_ = exec.CommandContext(ctx, pyBin, "-c", script, dbPath, sid).Run()
		}
	}
	return nil
}

func cleanKimiSession(userHome, sid string) error {
	for _, root := range []string{
		filepath.Join(userHome, ".kimi-code", "sessions"),
		filepath.Join(userHome, ".kimi", "sessions"),
	} {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.Name() == sid || d.Name() == sid+".json" || d.Name() == sid+".jsonl" {
				_ = os.RemoveAll(p)
				if d.IsDir() {
					return filepath.SkipDir
				}
			}
			return nil
		})
	}
	return nil
}

func cleanOpenCodeSession(userHome, sid string) error {
	for _, root := range []string{
		filepath.Join(userHome, ".local", "share", "opencode", "storage", "session"),
		filepath.Join(userHome, ".opencode", "sessions"),
	} {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.Name() == sid || d.Name() == sid+".json" || strings.Contains(d.Name(), sid) {
				_ = os.RemoveAll(p)
				if d.IsDir() {
					return filepath.SkipDir
				}
			}
			return nil
		})
	}
	return nil
}
