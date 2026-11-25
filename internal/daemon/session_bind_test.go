package daemon

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestSessionBindingDiscoveryCodexCursorAndUUID(t *testing.T) {
	// 1. UUIDv4 format check for Claude pre-assignment.
	u1 := newUUIDv4()
	u2 := newUUIDv4()
	if len(u1) != 36 || u1 == u2 || u1[14] != '4' {
		t.Fatalf("invalid uuid v4: u1=%q u2=%q", u1, u2)
	}

	home := t.TempDir()
	workDir := filepath.Join(home, "project", "finance")
	_ = os.MkdirAll(workDir, 0o755)

	// 2. Codex rollout session_meta discovery scoped by cwd.
	today := time.Now()
	codexDir := filepath.Join(home, ".codex", "sessions", today.Format("2006"), today.Format("01"), today.Format("02"))
	_ = os.MkdirAll(codexDir, 0o755)
	_ = os.WriteFile(filepath.Join(codexDir, "rollout-1.jsonl"),
		[]byte(`{"type":"session_meta","payload":{"id":"codex-sess-match","cwd":"`+workDir+`"}}`+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(codexDir, "rollout-2.jsonl"),
		[]byte(`{"type":"session_meta","payload":{"id":"codex-sess-other","cwd":"/tmp/other"}}`+"\n"), 0o644)

	codexIDs := listCodexSessionIDs(home, workDir)
	if !slices.Contains(codexIDs, "codex-sess-match") || slices.Contains(codexIDs, "codex-sess-other") {
		t.Fatalf("unexpected codex session IDs: %v", codexIDs)
	}

	// 3. Cursor MD5 workspace chat directory discovery.
	sum := md5.Sum([]byte(cleanAbs(workDir)))
	wsHash := hex.EncodeToString(sum[:])
	chatDir := filepath.Join(home, ".cursor", "chats", wsHash, "6e641047-7560-41f3-8bde-3ed5d5b9a715")
	_ = os.MkdirAll(chatDir, 0o755)

	cursorIDs := listCursorChatIDs(home, workDir)
	if !slices.Contains(cursorIDs, "6e641047-7560-41f3-8bde-3ed5d5b9a715") {
		t.Fatalf("unexpected cursor chat IDs: %v", cursorIDs)
	}

	// 4. ListCLISessions discovers titles & prioritizes SameDir for codex, claude, cursor.
	_ = os.WriteFile(filepath.Join(home, ".codex", "history.jsonl"),
		[]byte(`{"session_id":"codex-sess-match","ts":1787364805,"text":"重构金融模块缓存"}`+"\n"), 0o644)
	codexList := ListCLISessions("codex", workDir, workDir, home, filepath.Join(home, ".aio"))
	if len(codexList) != 2 || codexList[0].ID != "codex-sess-match" || !codexList[0].SameDir || codexList[0].Title != "重构金融模块缓存" {
		t.Fatalf("unexpected codex session list: %+v", codexList)
	}

	_ = os.WriteFile(filepath.Join(chatDir, "meta.json"),
		[]byte(`{"title":"Code Review Discussion","cwd":"`+workDir+`","updatedAtMs":1794972747493}`), 0o644)
	cursorList := ListCLISessions("cursor", workDir, workDir, home, filepath.Join(home, ".aio"))
	if len(cursorList) != 1 || cursorList[0].Title != "Code Review Discussion" || !cursorList[0].SameDir {
		t.Fatalf("unexpected cursor session list: %+v", cursorList)
	}

	claudeDir := filepath.Join(home, ".claude", "projects", "-tmp-test-proj")
	_ = os.MkdirAll(claudeDir, 0o755)
	_ = os.WriteFile(filepath.Join(claudeDir, "3cf1d311-b51b-4373-8b61-fc9496fc93c5.jsonl"),
		[]byte(`{"type":"user","cwd":"`+workDir+`","message":{"role":"user","content":"排查慢查询问题"}}`+"\n"), 0o644)
	claudeList := ListCLISessions("claude", workDir, workDir, home, filepath.Join(home, ".aio"))
	if len(claudeList) != 1 || claudeList[0].ID != "3cf1d311-b51b-4373-8b61-fc9496fc93c5" || claudeList[0].Title != "排查慢查询问题" || !claudeList[0].SameDir {
		t.Fatalf("unexpected claude session list: %+v", claudeList)
	}
}
