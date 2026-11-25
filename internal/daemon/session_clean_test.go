package daemon

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateSessionIDRejectsTraversal(t *testing.T) {
	bad := []string{"", "short", "../etc/passwd", "a/b/c", `a\b`, "abc..def"}
	for _, s := range bad {
		if err := ValidateSessionID(s); err == nil {
			t.Fatalf("expected ValidateSessionID(%q) to fail", s)
		}
	}
	good := []string{
		"11111111-2222-4333-8444-555555555555",
		"01a20c3c-c7ae-7380-be20-a775628d284f",
		"ses_abc12345",
	}
	for _, s := range good {
		if err := ValidateSessionID(s); err != nil {
			t.Fatalf("expected ValidateSessionID(%q) to succeed, got %v", s, err)
		}
	}
}

func TestCleanCLISessionRemovesOnlyTargetSession(t *testing.T) {
	home := t.TempDir()
	workDir := filepath.Join(home, "repo")
	sid := "11111111-2222-4333-8444-555555555555"
	keepSID := "99999999-2222-4333-8444-555555555555"

	// 1. Claude JSONL
	enc := strings.ReplaceAll(cleanAbs(workDir), "/", "-")
	claudeDir := filepath.Join(home, ".claude", "projects", enc)
	_ = os.MkdirAll(claudeDir, 0o755)
	claudeTarget := filepath.Join(claudeDir, sid+".jsonl")
	claudeKeep := filepath.Join(claudeDir, keepSID+".jsonl")
	_ = os.WriteFile(claudeTarget, []byte("{}\n"), 0o644)
	_ = os.WriteFile(claudeKeep, []byte("{}\n"), 0o644)
	if err := CleanCLISession("claude", workDir, sid, home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(claudeTarget); !os.IsNotExist(err) {
		t.Fatalf("expected claude session file to be removed")
	}
	if _, err := os.Stat(claudeKeep); err != nil {
		t.Fatalf("expected sibling claude session to be preserved")
	}

	// 2. Codex rollout JSONL
	codexDir := filepath.Join(home, ".codex", "sessions", "2026", "11", "24")
	_ = os.MkdirAll(codexDir, 0o755)
	codexTarget := filepath.Join(codexDir, "rollout-2026-11-24-"+sid+".jsonl")
	_ = os.WriteFile(codexTarget, []byte("{}\n"), 0o644)
	if err := CleanCLISession("codex", workDir, sid, home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(codexTarget); !os.IsNotExist(err) {
		t.Fatalf("expected codex rollout file to be removed")
	}

	// 3. Cursor chat dir
	sum := md5.Sum([]byte(cleanAbs(workDir)))
	cursorTarget := filepath.Join(home, ".cursor", "chats", hex.EncodeToString(sum[:]), sid)
	_ = os.MkdirAll(cursorTarget, 0o755)
	_ = os.WriteFile(filepath.Join(cursorTarget, "meta.json"), []byte("{}"), 0o644)
	if err := CleanCLISession("cursor", workDir, sid, home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cursorTarget); !os.IsNotExist(err) {
		t.Fatalf("expected cursor chat directory to be removed")
	}

	// 4. OpenCode session file
	ocDir := filepath.Join(home, ".local", "share", "opencode", "storage", "session", "proj")
	_ = os.MkdirAll(ocDir, 0o755)
	ocTarget := filepath.Join(ocDir, sid+".json")
	_ = os.WriteFile(ocTarget, []byte(`{"id":"`+sid+`"}`), 0o644)
	if err := CleanCLISession("opencode", workDir, sid, home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ocTarget); !os.IsNotExist(err) {
		t.Fatalf("expected opencode session file to be removed")
	}
}
