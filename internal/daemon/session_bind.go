package daemon

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/loucongV/all-in-one/internal/protocol"
)

// newUUIDv4 generates an RFC 4122 version 4 UUID string (used to deterministically
// pre-bind Claude Code sessions via `--session-id <uuid>`).
func newUUIDv4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// preAssignSessionID returns an upfront session ID for agents that accept deterministic
// session IDs on creation (currently Claude Code via --session-id).
func preAssignSessionID(agent string) string {
	if agent == "claude" {
		return newUUIDv4()
	}
	return ""
}

// snapshotSessionIDs captures currently known on-disk session IDs for a given agent+workDir
// right before launching a new PTY so watchAndBindSession can identify the newly created one.
func snapshotSessionIDs(agent, workDir, userHome string) map[string]struct{} {
	if userHome == "" {
		userHome, _ = os.UserHomeDir()
	}
	out := make(map[string]struct{})
	switch agent {
	case "codex":
		for _, id := range listCodexSessionIDs(userHome, workDir) {
			out[id] = struct{}{}
		}
	case "cursor":
		for _, id := range listCursorChatIDs(userHome, workDir) {
			out[id] = struct{}{}
		}
	}
	return out
}

// watchAndBindSession asynchronously watches for the newly created on-disk session ID
// (for Codex rollouts and Cursor chat directories) and binds it to the Run record so
// future auto-resumes always reconnect to this task's exact conversation.
func (s *Server) watchAndBindSession(runID, agent, workDir string, existing map[string]struct{}) {
	if agent != "codex" && agent != "cursor" {
		return
	}
	userHome, err := os.UserHomeDir()
	if err != nil || userHome == "" {
		return
	}

	go func() {
		ticker := time.NewTicker(1500 * time.Millisecond)
		defer ticker.Stop()
		deadline := time.Now().Add(45 * time.Second)

		for time.Now().Before(deadline) {
			<-ticker.C
			run, ok := s.store.GetRun(runID)
			if !ok || run.SessionID != "" {
				return
			}
			if sess, ok := s.pty.Get(runID); !ok || sess.IsClosed() {
				return
			}

			var candidates []string
			switch agent {
			case "codex":
				candidates = listCodexSessionIDs(userHome, workDir)
			case "cursor":
				candidates = listCursorChatIDs(userHome, workDir)
			}

			for _, id := range candidates {
				if _, existed := existing[id]; existed {
					continue
				}
				if s.bindSessionID(runID, id) {
					return
				}
			}
		}
	}()
}

func (s *Server) bindSessionID(runID, sessionID string) bool {
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return false
	}
	run, ok := s.store.GetRun(runID)
	if !ok {
		return false
	}
	if run.SessionID == sid {
		return true
	}
	run.SessionID = sid
	run.UpdatedAt = time.Now()
	if err := s.store.UpdateRun(run); err != nil {
		return false
	}
	s.broadcast(protocol.DaemonEvent{
		Type:  "run_status",
		RunID: runID,
		Run:   &run,
		At:    run.UpdatedAt,
	})
	return true
}

// listCodexSessionIDs inspects today's and yesterday's ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl
// files and returns session IDs whose session_meta.payload.cwd matches workDir.
func listCodexSessionIDs(userHome, workDir string) []string {
	absWork := cleanAbs(workDir)
	now := time.Now()
	days := []time.Time{now, now.AddDate(0, 0, -1)}

	var ids []string
	seen := make(map[string]struct{})
	for _, day := range days {
		dir := filepath.Join(userHome, ".codex", "sessions",
			day.Format("2006"), day.Format("01"), day.Format("02"))
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasPrefix(e.Name(), "rollout-") || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			sid, cwd := readCodexRolloutMeta(filepath.Join(dir, e.Name()))
			if sid == "" {
				continue
			}
			if absWork != "" && cleanAbs(cwd) != absWork {
				continue
			}
			if _, dup := seen[sid]; !dup {
				seen[sid] = struct{}{}
				ids = append(ids, sid)
			}
		}
	}
	return ids
}

func readCodexRolloutMeta(path string) (sessionID, cwd string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 512*1024)
	if !sc.Scan() {
		return "", ""
	}
	var line struct {
		Type    string `json:"type"`
		Payload struct {
			SessionID string `json:"session_id"`
			ID        string `json:"id"`
			CWD       string `json:"cwd"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
		return "", ""
	}
	if line.Type != "session_meta" {
		return "", ""
	}
	sid := strings.TrimSpace(line.Payload.SessionID)
	if sid == "" {
		sid = strings.TrimSpace(line.Payload.ID)
	}
	return sid, strings.TrimSpace(line.Payload.CWD)
}

// listCursorChatIDs lists chat UUID directories under ~/.cursor/chats/<md5(absWorkDir)>/.
func listCursorChatIDs(userHome, workDir string) []string {
	absWork := cleanAbs(workDir)
	if absWork == "" {
		return nil
	}
	sum := md5.Sum([]byte(absWork))
	wsHash := hex.EncodeToString(sum[:])
	dir := filepath.Join(userHome, ".cursor", "chats", wsHash)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() && len(e.Name()) >= 8 {
			ids = append(ids, e.Name())
		}
	}
	return ids
}

func cleanAbs(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(p)
}

// ListCLISessions discovers existing local conversations/sessions for the given agent,
// prioritizing sessions matching workDir or projDir, excluding internal aio probe dirs.
func ListCLISessions(agent, workDir, projDir, userHome, aioHome string) []protocol.CLISessionInfo {
	if userHome == "" {
		userHome, _ = os.UserHomeDir()
	}
	absWork := cleanAbs(workDir)
	absProj := cleanAbs(projDir)
	probeDir := ""
	if aioHome != "" {
		probeDir = cleanAbs(filepath.Join(aioHome, "probe"))
	}

	isSameDir := func(ws string) bool {
		c := cleanAbs(ws)
		if c == "" {
			return false
		}
		return (absWork != "" && c == absWork) || (absProj != "" && c == absProj)
	}
	isProbeDir := func(ws string) bool {
		c := cleanAbs(ws)
		if c == "" {
			return false
		}
		if probeDir != "" && c == probeDir {
			return true
		}
		return strings.Contains(c, "/.aio/probe") || strings.Contains(c, "/aio-probe-")
	}

	var list []protocol.CLISessionInfo
	switch agent {
	case "agy":
		list = discoverAgySessions(userHome, isSameDir, isProbeDir)
	case "codex":
		list = discoverCodexSessions(userHome, isSameDir, isProbeDir)
	case "claude":
		list = discoverClaudeSessions(userHome, absWork, absProj, isSameDir, isProbeDir)
	case "cursor":
		list = discoverCursorSessions(userHome, isSameDir, isProbeDir)
	case "kimi":
		list = discoverKimiSessions(userHome, isSameDir, isProbeDir)
	case "opencode":
		list = discoverOpenCodeSessions(userHome, isSameDir, isProbeDir)
	}

	sort.SliceStable(list, func(i, j int) bool {
		if list[i].SameDir != list[j].SameDir {
			return list[i].SameDir
		}
		return list[i].UpdatedAt.After(list[j].UpdatedAt)
	})
	if len(list) > 30 {
		list = list[:30]
	}
	return list
}

func discoverAgySessions(userHome string, isSameDir, isProbeDir func(string) bool) []protocol.CLISessionInfo {
	dbPath := filepath.Join(userHome, ".gemini", "antigravity-cli", "conversation_summaries.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil
	}
	pyBin, err := exec.LookPath("python3")
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	script := `import json, sqlite3, sys
db = sys.argv[1]
con = sqlite3.connect(f"file:{db}?mode=ro", uri=True, timeout=1.0)
cur = con.cursor()
rows = cur.execute("SELECT conversation_id, title, preview, last_modified_time, workspace_uris FROM conversation_summaries WHERE killed=0 ORDER BY last_modified_time DESC LIMIT 80").fetchall()
out = []
for cid, title, preview, ts, uris in rows:
    out.append({"id": cid, "title": title or preview or "", "ts": str(ts or ""), "uris": uris or "[]"})
print(json.dumps(out))`

	cmd := exec.CommandContext(ctx, pyBin, "-c", script, dbPath)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		return nil
	}

	var rows []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		TS    string `json:"ts"`
		URIs  string `json:"uris"`
	}
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		return nil
	}

	var out []protocol.CLISessionInfo
	for _, r := range rows {
		ws := ""
		var uris []string
		if json.Unmarshal([]byte(r.URIs), &uris) == nil && len(uris) > 0 {
			ws = strings.TrimPrefix(uris[0], "file://")
		}
		if isProbeDir(ws) {
			continue
		}
		t := parseFlexibleTime(r.TS)
		title := singleLinePreview(r.Title, 64)
		if title == "" {
			title = "(无标题会话)"
		}
		out = append(out, protocol.CLISessionInfo{
			ID:        r.ID,
			Title:     title,
			Workspace: ws,
			UpdatedAt: t,
			SameDir:   isSameDir(ws),
		})
	}
	return out
}

func discoverCodexSessions(userHome string, isSameDir, isProbeDir func(string) bool) []protocol.CLISessionInfo {
	// Load prompt previews from ~/.codex/history.jsonl keyed by session_id.
	histTitle := make(map[string]string)
	if f, err := os.Open(filepath.Join(userHome, ".codex", "history.jsonl")); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			var h struct {
				SessionID string `json:"session_id"`
				Text      string `json:"text"`
			}
			if json.Unmarshal(sc.Bytes(), &h) == nil && h.SessionID != "" {
				txt := singleLinePreview(h.Text, 64)
				if txt != "" && !strings.HasPrefix(txt, "!") && !strings.HasPrefix(txt, "/") {
					if _, exists := histTitle[h.SessionID]; !exists {
						histTitle[h.SessionID] = txt
					}
				}
			}
		}
		_ = f.Close()
	}

	root := filepath.Join(userHome, ".codex", "sessions")
	type fileEntry struct {
		path string
		mod  time.Time
	}
	var files []fileEntry
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasPrefix(d.Name(), "rollout-") && strings.HasSuffix(d.Name(), ".jsonl") {
			if info, err := d.Info(); err == nil {
				files = append(files, fileEntry{path: p, mod: info.ModTime()})
			}
		}
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	if len(files) > 60 {
		files = files[:60]
	}

	seen := make(map[string]bool)
	var out []protocol.CLISessionInfo
	for _, fe := range files {
		sid, cwd := readCodexRolloutMeta(fe.path)
		if sid == "" || seen[sid] || isProbeDir(cwd) {
			continue
		}
		seen[sid] = true
		title := histTitle[sid]
		if title == "" {
			title = "(Codex 会话)"
		}
		out = append(out, protocol.CLISessionInfo{
			ID:        sid,
			Title:     title,
			Workspace: cwd,
			UpdatedAt: fe.mod,
			SameDir:   isSameDir(cwd),
		})
	}
	return out
}

func discoverClaudeSessions(userHome, absWork, absProj string, isSameDir, isProbeDir func(string) bool) []protocol.CLISessionInfo {
	projRoot := filepath.Join(userHome, ".claude", "projects")
	dirs, err := os.ReadDir(projRoot)
	if err != nil {
		return nil
	}

	encodeClaudeDir := func(p string) string {
		if p == "" {
			return ""
		}
		return strings.ReplaceAll(p, "/", "-")
	}
	wantDirs := map[string]bool{}
	if enc := encodeClaudeDir(absWork); enc != "" {
		wantDirs[enc] = true
	}
	if enc := encodeClaudeDir(absProj); enc != "" {
		wantDirs[enc] = true
	}

	var out []protocol.CLISessionInfo
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dirName := d.Name()
		if strings.Contains(dirName, "aio-probe") || strings.Contains(dirName, "claude-upgrade") {
			continue
		}
		same := wantDirs[dirName]
		fullDir := filepath.Join(projRoot, dirName)
		entries, err := os.ReadDir(fullDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			sid := strings.TrimSuffix(e.Name(), ".jsonl")
			if len(sid) < 8 {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			title, cwd := readClaudeSessionSummary(filepath.Join(fullDir, e.Name()))
			if isProbeDir(cwd) {
				continue
			}
			if !same && cwd != "" {
				same = isSameDir(cwd)
			}
			if title == "" {
				title = "(Claude 会话)"
			}
			ws := cwd
			if ws == "" {
				ws = dirName
			}
			out = append(out, protocol.CLISessionInfo{
				ID:        sid,
				Title:     title,
				Workspace: ws,
				UpdatedAt: info.ModTime(),
				SameDir:   same,
			})
		}
	}
	return out
}

func readClaudeSessionSummary(path string) (title, cwd string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	linesRead := 0
	for sc.Scan() && linesRead < 80 {
		linesRead++
		var row struct {
			Type        string `json:"type"`
			CWD         string `json:"cwd"`
			Summary     string `json:"summary"`
			CustomTitle string `json:"customTitle"`
			Message     struct {
				Content any `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &row) != nil {
			continue
		}
		if cwd == "" && row.CWD != "" {
			cwd = row.CWD
		}
		if row.CustomTitle != "" {
			title = singleLinePreview(row.CustomTitle, 64)
		} else if row.Summary != "" && title == "" {
			title = singleLinePreview(row.Summary, 64)
		} else if row.Type == "user" && title == "" {
			if s, ok := row.Message.Content.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" && !strings.HasPrefix(s, "<local-command") && !strings.HasPrefix(s, "<command-name>") {
					title = singleLinePreview(s, 64)
				}
			}
		}
	}
	return title, cwd
}

func discoverCursorSessions(userHome string, isSameDir, isProbeDir func(string) bool) []protocol.CLISessionInfo {
	chatsRoot := filepath.Join(userHome, ".cursor", "chats")
	wsDirs, err := os.ReadDir(chatsRoot)
	if err != nil {
		return nil
	}
	var out []protocol.CLISessionInfo
	for _, wd := range wsDirs {
		if !wd.IsDir() {
			continue
		}
		chatDirs, err := os.ReadDir(filepath.Join(chatsRoot, wd.Name()))
		if err != nil {
			continue
		}
		for _, cd := range chatDirs {
			if !cd.IsDir() || len(cd.Name()) < 8 {
				continue
			}
			chatPath := filepath.Join(chatsRoot, wd.Name(), cd.Name())
			metaPath := filepath.Join(chatPath, "meta.json")
			var meta struct {
				Title       string `json:"title"`
				CWD         string `json:"cwd"`
				UpdatedAtMs int64  `json:"updatedAtMs"`
				CreatedAtMs int64  `json:"createdAtMs"`
			}
			if raw, err := os.ReadFile(metaPath); err == nil {
				_ = json.Unmarshal(raw, &meta)
			}
			if isProbeDir(meta.CWD) {
				continue
			}
			var ts time.Time
			if meta.UpdatedAtMs > 0 {
				ts = time.UnixMilli(meta.UpdatedAtMs)
			} else if meta.CreatedAtMs > 0 {
				ts = time.UnixMilli(meta.CreatedAtMs)
			} else if info, err := cd.Info(); err == nil {
				ts = info.ModTime()
			}
			title := singleLinePreview(meta.Title, 64)
			if title == "" {
				title = "(Cursor 会话)"
			}
			out = append(out, protocol.CLISessionInfo{
				ID:        cd.Name(),
				Title:     title,
				Workspace: meta.CWD,
				UpdatedAt: ts,
				SameDir:   isSameDir(meta.CWD),
			})
		}
	}
	return out
}

func discoverKimiSessions(userHome string, isSameDir, isProbeDir func(string) bool) []protocol.CLISessionInfo {
	var out []protocol.CLISessionInfo
	seen := make(map[string]bool)
	for _, root := range []string{
		filepath.Join(userHome, ".kimi-code", "sessions"),
		filepath.Join(userHome, ".kimi", "sessions"),
	} {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			sid := strings.TrimSuffix(strings.TrimSuffix(name, ".jsonl"), ".json")
			if len(sid) < 6 || seen[sid] {
				continue
			}
			seen[sid] = true
			var ts time.Time
			if info, err := e.Info(); err == nil {
				ts = info.ModTime()
			}
			out = append(out, protocol.CLISessionInfo{
				ID:        sid,
				Title:     "(Kimi 会话)",
				UpdatedAt: ts,
				SameDir:   false,
			})
		}
	}
	return out
}

func discoverOpenCodeSessions(userHome string, isSameDir, isProbeDir func(string) bool) []protocol.CLISessionInfo {
	root := filepath.Join(userHome, ".local", "share", "opencode", "storage", "session")
	if _, err := os.Stat(root); err != nil {
		return nil
	}
	var out []protocol.CLISessionInfo
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		var meta struct {
			ID        string `json:"id"`
			Title     string `json:"title"`
			Directory string `json:"directory"`
			Time      struct {
				Updated int64 `json:"updated"`
				Created int64 `json:"created"`
			} `json:"time"`
		}
		if json.Unmarshal(raw, &meta) != nil || meta.ID == "" {
			return nil
		}
		if isProbeDir(meta.Directory) {
			return nil
		}
		var ts time.Time
		if meta.Time.Updated > 0 {
			ts = time.UnixMilli(meta.Time.Updated)
		} else if meta.Time.Created > 0 {
			ts = time.UnixMilli(meta.Time.Created)
		} else if info, err := d.Info(); err == nil {
			ts = info.ModTime()
		}
		title := singleLinePreview(meta.Title, 64)
		if title == "" {
			title = "(OpenCode 会话)"
		}
		out = append(out, protocol.CLISessionInfo{
			ID:        meta.ID,
			Title:     title,
			Workspace: meta.Directory,
			UpdatedAt: ts,
			SameDir:   isSameDir(meta.Directory),
		})
		return nil
	})
	return out
}

func parseFlexibleTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999+00:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func singleLinePreview(s string, maxRunes int) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes-1]) + "…"
	}
	return s
}
