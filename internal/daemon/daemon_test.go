package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/loucongV/all-in-one/internal/adapter"
	"github.com/loucongV/all-in-one/internal/protocol"
	"github.com/loucongV/all-in-one/internal/store"
)

type mockShellAdapter struct {
	name string
}

func (m *mockShellAdapter) Name() string                  { return m.name }
func (m *mockShellAdapter) Label() string                 { return "Mock " + m.name }
func (m *mockShellAdapter) Binary() string                { return "sh" }
func (m *mockShellAdapter) ResolveBinary() (string, bool) { return "/bin/sh", true }

func (m *mockShellAdapter) BuildLaunch(spec adapter.LaunchSpec) (adapter.CommandSpec, error) {
	// Writes prompt to a file in the isolated worktree, echoes greeting, then waits for input.
	script := `printf "agent=%s prompt=%s\n" "$AIO_AGENT" "$AIO_PROMPT" > result.txt; echo "READY:$AIO_AGENT:$AIO_PROMPT"; read line; echo "GOT:$line"; sleep 5`
	return adapter.CommandSpec{
		Binary: "sh",
		Args:   []string{"-c", script},
		Env: []string{
			"AIO_RUN_ID=" + spec.RunID,
			"AIO_AGENT=" + m.name,
			"AIO_PROMPT=" + spec.Prompt,
		},
		Dir: spec.WorkDir,
	}, nil
}

func (m *mockShellAdapter) BuildResume(spec adapter.LaunchSpec) (adapter.CommandSpec, error) {
	return m.BuildLaunch(spec)
}

func TestDaemonComparePTYHooksAndAdoptEndToEnd(t *testing.T) {
	// 1. Setup isolated git repo and AIO home.
	home := t.TempDir()
	repo := t.TempDir()
	mustGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, string(out))
		}
	}
	mustGit("init", "-b", "main")
	mustGit("config", "user.email", "test@aio.local")
	mustGit("config", "user.name", "AIO Test")
	_ = os.WriteFile(filepath.Join(repo, "app.txt"), []byte("base\n"), 0o644)
	mustGit("add", "app.txt")
	mustGit("commit", "-m", "init")

	st, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	reg := adapter.DefaultRegistry()
	reg.Register(&mockShellAdapter{name: "mock-a"})
	reg.Register(&mockShellAdapter{name: "mock-b"})

	sock := filepath.Join(home, "daemon.sock")
	srv := NewServer(st, reg, sock, "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = srv.Start(ctx)
	}()

	client := NewClient(sock)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if client.Health() == nil {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if err := client.Health(); err != nil {
		t.Fatalf("daemon did not become healthy: %v", err)
	}
	if hi, err := client.HealthInfo(); err != nil || hi.Version == "" {
		t.Fatalf("expected daemon HealthInfo to report version, got %+v err=%v", hi, err)
	}

	// 2. Register project & launch a 2-agent Compare Task with auto-sent prompt.
	proj, err := client.AddProject(protocol.AddProjectRequest{RepoPath: repo})
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}

	created, err := client.CreateTask(protocol.CreateTaskRequest{
		ProjectID: proj.ID,
		Title:     "compare-demo",
		Prompt:    "build-it",
		Agents:    []string{"mock-a", "mock-b"},
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if created.Task.Mode != "compare" || len(created.Runs) != 2 {
		t.Fatalf("expected compare task with 2 runs, got %+v", created)
	}
	renamed, err := client.RenameTask(created.Task.ID, "compare-renamed")
	if err != nil || renamed.Title != "compare-renamed" {
		t.Fatalf("RenameTask failed: task=%+v err=%v", renamed, err)
	}

	// 3. Attach to mock-a PTY over WebSocket, send input, verify VT screen renders response!
	ws, err := client.ConnectPTY(created.Runs[0].ID, false)
	if err != nil {
		t.Fatalf("ConnectPTY: %v", err)
	}
	defer ws.Close()

	if err := SendPTYInput(ws, []byte("hello-world\r")); err != nil {
		t.Fatalf("SendPTYInput: %v", err)
	}

	sawOutput := false
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	for !sawOutput {
		var msg protocol.PTYMessage
		if err := ws.ReadJSON(&msg); err != nil {
			break
		}
		if strings.Contains(msg.Data, "GOT:hello-world") {
			sawOutput = true
		}
	}
	if !sawOutput {
		t.Fatalf("did not observe PTY VT output containing GOT:hello-world")
	}

	// 4. Report hook event (waiting_input) and verify state reflects it.
	if err := client.SendHook(protocol.HookEventRequest{
		RunID:  created.Runs[1].ID,
		Event:  "waiting",
		Detail: "needs approval",
	}); err != nil {
		t.Fatalf("SendHook: %v", err)
	}
	snap, err := client.State()
	if err != nil {
		t.Fatal(err)
	}
	foundWaiting := false
	for _, r := range snap.Runs {
		if r.ID == created.Runs[1].ID && r.Status == protocol.StatusWaitingInput {
			foundWaiting = true
		}
	}
	if !foundWaiting {
		t.Fatalf("expected run %s to be waiting_input after hook, state=%+v", created.Runs[1].ID, snap.Runs)
	}

	// Verify AckRun clears waiting_input back to running.
	if err := client.AckRun(created.Runs[1].ID); err != nil {
		t.Fatalf("AckRun: %v", err)
	}
	snap, err = client.State()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range snap.Runs {
		if r.ID == created.Runs[1].ID && r.Status != protocol.StatusRunning {
			t.Fatalf("expected run %s to return to running after AckRun, got %s", r.ID, r.Status)
		}
	}

	// Verify Antigravity lifecycle hooks: boot-time agy_idle stays running; after agy:working -> agy_idle transitions to done; next turn returns to running.
	runID := created.Runs[0].ID
	if err := client.SendHook(protocol.HookEventRequest{RunID: runID, Event: "agy_idle", Detail: "agy:idle"}); err != nil {
		t.Fatalf("SendHook boot agy_idle: %v", err)
	}
	snap, _ = client.State()
	for _, r := range snap.Runs {
		if r.ID == runID && r.Status != protocol.StatusRunning {
			t.Fatalf("expected boot-time agy_idle to stay running, got %s (%s)", r.Status, r.StatusDetail)
		}
	}
	if err := client.SendHook(protocol.HookEventRequest{RunID: runID, Event: "running", Detail: "agy:working"}); err != nil {
		t.Fatalf("SendHook agy:working: %v", err)
	}
	if err := client.SendHook(protocol.HookEventRequest{RunID: runID, Event: "agy_idle", Detail: "agy:idle"}); err != nil {
		t.Fatalf("SendHook post-work agy_idle: %v", err)
	}
	snap, _ = client.State()
	for _, r := range snap.Runs {
		if r.ID == runID && r.Status != protocol.StatusDone {
			t.Fatalf("expected agy_idle after working to transition to done, got %s (%s)", r.Status, r.StatusDetail)
		}
	}
	if err := client.SendHook(protocol.HookEventRequest{RunID: runID, Event: "running", Detail: "agy:working"}); err != nil {
		t.Fatalf("SendHook second turn agy:working: %v", err)
	}
	snap, _ = client.State()
	for _, r := range snap.Runs {
		if r.ID == runID && (r.Status != protocol.StatusRunning || r.FinishedAt != nil) {
			t.Fatalf("expected second turn agy:working to return to running with nil FinishedAt, got %s finished=%v", r.Status, r.FinishedAt)
		}
	}

	// Verify multi-turn prompt submit (Enter on a done run) flips status back to running (e.g. Codex Turn 2).
	if err := client.SendHook(protocol.HookEventRequest{RunID: runID, Event: "done", Detail: "hook:done"}); err != nil {
		t.Fatalf("SendHook done: %v", err)
	}
	if err := SendPTYInput(ws, []byte("next prompt\r")); err != nil {
		t.Fatalf("SendPTYInput turn 2: %v", err)
	}
	deadlineStatus := time.Now().Add(2 * time.Second)
	flippedRunning := false
	for time.Now().Before(deadlineStatus) {
		snap, _ = client.State()
		for _, r := range snap.Runs {
			if r.ID == runID && r.Status == protocol.StatusRunning {
				flippedRunning = true
			}
		}
		if flippedRunning {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !flippedRunning {
		t.Fatalf("expected pressing Enter on a done run to flip status back to running")
	}

	// 5. Inspect Compare Diff and Adopt mock-a winner (squash merge + clean losers).
	diffResp, err := client.TaskDiff(created.Task.ID)
	if err != nil {
		t.Fatalf("TaskDiff: %v", err)
	}
	if len(diffResp.Runs) != 2 || diffResp.Runs[0].FilesChanged == 0 {
		t.Fatalf("expected diff stats for both runs, got %+v", diffResp.Runs)
	}

	adoptResp, err := client.AdoptTask(created.Task.ID, protocol.AdoptTaskRequest{
		WinnerRunID: created.Runs[0].ID,
		Strategy:    protocol.MergeSquash,
	})
	if err != nil {
		t.Fatalf("AdoptTask: %v", err)
	}
	if adoptResp.CleanedWorktrees != 2 {
		t.Fatalf("expected 2 cleaned worktrees, got %+v", adoptResp)
	}

	// Verify winner's file landed in main repo.
	resBytes, err := os.ReadFile(filepath.Join(repo, "result.txt"))
	if err != nil || !strings.Contains(string(resBytes), "agent=mock-a prompt=build-it") {
		t.Fatalf("expected adopted winner result.txt in main repo, got %q err=%v", string(resBytes), err)
	}

	// 6. Verify plain non-git local directory works safely (add -> run -> delete without deleting the directory!).
	plainDir := t.TempDir()
	keepFile := filepath.Join(plainDir, "keep.txt")
	_ = os.WriteFile(keepFile, []byte("precious\n"), 0o644)

	plainProj, err := client.AddProject(protocol.AddProjectRequest{RepoPath: plainDir})
	if err != nil {
		t.Fatalf("AddProject plainDir: %v", err)
	}
	if plainProj.IsGitRepo {
		t.Fatalf("expected plainDir IsGitRepo=false")
	}
	plainTask, err := client.CreateTask(protocol.CreateTaskRequest{
		ProjectID: plainProj.ID,
		Title:     "plain-task",
		Prompt:    "hi",
		Agents:    []string{"mock-a"},
	})
	if err != nil {
		t.Fatalf("CreateTask plainDir: %v", err)
	}
	if plainTask.Runs[0].WorktreePath != plainDir || plainTask.Runs[0].Branch != "" {
		t.Fatalf("expected plain task to run directly in plainDir, got %+v", plainTask.Runs[0])
	}
	// Add a second agent to the existing task, then delete one run, then delete the remaining run (auto-cleans empty task).
	addedRuns, err := client.AddTaskRuns(plainTask.Task.ID, protocol.AddRunsRequest{
		Agents: []string{"mock-b"},
		Prompt: "second",
	})
	if err != nil || len(addedRuns) != 1 {
		t.Fatalf("AddTaskRuns failed: runs=%+v err=%v", addedRuns, err)
	}
	if err := client.DeleteRun(addedRuns[0].ID); err != nil {
		t.Fatalf("DeleteRun failed: %v", err)
	}
	if err := client.DeleteRun(plainTask.Runs[0].ID); err != nil {
		t.Fatalf("DeleteRun last run failed: %v", err)
	}
	snapAfter, err := client.State()
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range snapAfter.Tasks {
		if tk.ID == plainTask.Task.ID {
			t.Fatalf("expected empty task %s to be auto-deleted after last run deleted", plainTask.Task.ID)
		}
	}
	if _, err := os.Stat(keepFile); err != nil {
		t.Fatalf("plain directory file was unexpectedly deleted: %v", err)
	}

	// 7. Verify shared Worktree handoff workflow (1 task = 1 worktree: Agent A -> add Agent B -> delete A preserves worktree -> Adopt cleans up!).
	sharedTask, err := client.CreateTask(protocol.CreateTaskRequest{
		ProjectID: proj.ID,
		Title:     "quota-handoff",
		Prompt:    "step1",
		Agents:    []string{"mock-a"},
		WorkMode:  "shared",
	})
	if err != nil {
		t.Fatalf("CreateTask shared: %v", err)
	}
	if sharedTask.Task.WorkMode != "shared" || sharedTask.Task.WorktreePath == "" || sharedTask.Task.WorktreePath == repo {
		t.Fatalf("expected shared worktree on task, got %+v", sharedTask.Task)
	}
	// Agent A writes a file in the shared worktree.
	sharedWT := sharedTask.Task.WorktreePath
	if err := os.WriteFile(filepath.Join(sharedWT, "handoff.txt"), []byte("from-A\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Agent A runs out of quota -> add Agent B to the SAME task!
	handoffRuns, err := client.AddTaskRuns(sharedTask.Task.ID, protocol.AddRunsRequest{
		Agents: []string{"mock-b"},
		Prompt: "step2",
	})
	if err != nil || len(handoffRuns) != 1 {
		t.Fatalf("AddTaskRuns shared failed: runs=%+v err=%v", handoffRuns, err)
	}
	if handoffRuns[0].WorktreePath != sharedWT || handoffRuns[0].Branch != sharedTask.Task.Branch {
		t.Fatalf("expected Agent B to reuse exact shared worktree %s (%s), got %s (%s)",
			sharedWT, sharedTask.Task.Branch, handoffRuns[0].WorktreePath, handoffRuns[0].Branch)
	}

	// Delete Agent A -> shared worktree MUST remain intact for Agent B!
	if err := client.DeleteRun(sharedTask.Runs[0].ID); err != nil {
		t.Fatalf("DeleteRun A in shared task: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sharedWT, "handoff.txt")); err != nil {
		t.Fatalf("shared worktree was deleted while Agent B still uses it: %v", err)
	}

	// Agent B appends to handoff.txt and adopts into main.
	if err := os.WriteFile(filepath.Join(sharedWT, "handoff.txt"), []byte("from-A\nfrom-B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sharedAdopt, err := client.AdoptTask(sharedTask.Task.ID, protocol.AdoptTaskRequest{
		WinnerRunID: handoffRuns[0].ID,
		Strategy:    protocol.MergeSquash,
	})
	if err != nil {
		t.Fatalf("AdoptTask shared: %v", err)
	}
	if sharedAdopt.CleanedWorktrees != 1 || len(sharedAdopt.Warnings) != 0 {
		t.Fatalf("expected 1 cleaned shared worktree with 0 warnings, got %+v", sharedAdopt)
	}
	finalHandoff, err := os.ReadFile(filepath.Join(repo, "handoff.txt"))
	if err != nil || string(finalHandoff) != "from-A\nfrom-B\n" {
		t.Fatalf("expected merged handoff.txt from A+B, got %q err=%v", string(finalHandoff), err)
	}

	// 8. Verify persistent workspace shell terminal (EnsureShell + interactive PTY WS + reuse).
	sh1, err := client.EnsureShell(repo)
	if err != nil || sh1.ShellID == "" {
		t.Fatalf("EnsureShell failed: %+v err=%v", sh1, err)
	}
	sh2, err := client.EnsureShell(repo)
	if err != nil || sh2.ShellID != sh1.ShellID {
		t.Fatalf("expected EnsureShell to reuse persistent shell_id %s, got %+v err=%v", sh1.ShellID, sh2, err)
	}
	shWS, err := client.ConnectPTY(sh1.ShellID, false)
	if err != nil {
		t.Fatalf("ConnectPTY shell: %v", err)
	}
	defer shWS.Close()
	if err := SendPTYInput(shWS, []byte("echo SHELL_READY_OK\r")); err != nil {
		t.Fatalf("SendPTYInput shell: %v", err)
	}
	sawShell := false
	_ = shWS.SetReadDeadline(time.Now().Add(3 * time.Second))
	for !sawShell {
		var msg protocol.PTYMessage
		if err := shWS.ReadJSON(&msg); err != nil {
			break
		}
		if strings.Contains(msg.Data, "SHELL_READY_OK") {
			sawShell = true
		}
	}
	if !sawShell {
		t.Fatalf("did not observe SHELL_READY_OK from workspace shell PTY")
	}
}

func TestPTYInputTrackerFiltersSlashCommandsAndEmptyEnter(t *testing.T) {
	var tr ptyInputTracker

	// Empty Enter (e.g. session picker / trust confirmation) must NOT count as a prompt submission.
	sub, hasEnter, cancel, escSeq, confirm := tr.Feed([]byte("\r"))
	if !hasEnter || cancel || escSeq || confirm || isRealPromptSubmission(sub) {
		t.Fatalf("empty Enter should not be treated as real prompt: sub=%q hasEnter=%v cancel=%v esc=%v confirm=%v", sub, hasEnter, cancel, escSeq, confirm)
	}

	// Arrow / focus escape sequences MUST be tagged as escSeq so they never flip waiting->running.
	_, hasEnter, cancel, escSeq, confirm = tr.Feed([]byte("\x1b[A"))
	if !escSeq || hasEnter || cancel || confirm {
		t.Fatalf("expected arrow key to be flagged as escape sequence")
	}
	_, _, _, escSeq, _ = tr.Feed([]byte("\x1b[I"))
	if !escSeq {
		t.Fatalf("expected focus-in report to be flagged as escape sequence")
	}

	// Single confirmation key on empty buffer is recognized without needing Enter.
	_, _, _, _, confirm = tr.Feed([]byte("y"))
	if !confirm {
		t.Fatalf("expected single 'y' to be flagged as confirm key")
	}
	// Reset buffer cleanly.
	tr.Feed([]byte{0x1b})

	// Slash command (/usage + Enter) must NOT flip an idle agent into running.
	tr.Feed([]byte("/usag"))
	tr.Feed([]byte("e"))
	sub, hasEnter, cancel, _, _ = tr.Feed([]byte("\r"))
	if !hasEnter || cancel || sub != "/usage" || isRealPromptSubmission(sub) {
		t.Fatalf("/usage should not be treated as real prompt: sub=%q hasEnter=%v", sub, hasEnter)
	}

	// Esc / Ctrl+C cancels and clears line buffer.
	tr.Feed([]byte("partial prompt"))
	_, _, cancel, _, _ = tr.Feed([]byte{0x1b})
	if !cancel {
		t.Fatalf("expected Esc to signal cancel")
	}
	sub, hasEnter, _, _, _ = tr.Feed([]byte("\r"))
	if !hasEnter || isRealPromptSubmission(sub) {
		t.Fatalf("buffer should be cleared after Esc, got %q", sub)
	}

	// Real prompt with backspace edit + Enter MUST count as a real prompt submission.
	tr.Feed([]byte("fix bux"))
	tr.Feed([]byte{0x7f})
	tr.Feed([]byte("g"))
	sub, hasEnter, cancel, _, _ = tr.Feed([]byte("\r"))
	if !hasEnter || cancel || sub != "fix bug" || !isRealPromptSubmission(sub) {
		t.Fatalf("expected real prompt 'fix bug', got sub=%q hasEnter=%v cancel=%v", sub, hasEnter, cancel)
	}

	// Pasted prompt + Enter MUST count as a real prompt submission.
	tr.AppendPaste("refactor module")
	sub, hasEnter, _, _, _ = tr.Feed([]byte("\r"))
	if !hasEnter || !isRealPromptSubmission(sub) {
		t.Fatalf("expected pasted prompt + Enter to be real prompt, got %q", sub)
	}

	// Native lifecycle agents & actionable waiting details checks.
	if !hasNativeRunningSignal("claude") || !hasNativeRunningSignal("agy") || !hasNativeRunningSignal("cursor") {
		t.Fatalf("expected claude/agy/cursor to be marked as native running-signal agents")
	}
	if hasNativeRunningSignal("codex") {
		t.Fatalf("codex does not have a native turn-start hook")
	}
	if isActionableWaitingDetail("terminal bell") || !isActionableWaitingDetail("claude:permission_prompt") {
		t.Fatalf("unexpected isActionableWaitingDetail classification")
	}
}

func TestDetectCodexTurnDoneFallback(t *testing.T) {
	// 1. Finished Codex turn (e.g. after image paste caused notify E2BIG).
	doneScreen := `
  这条 INFO 日志表示该规则条件不满足。

  Worked for 1m 13s • 15:26

› Ask Codex to do anything

  GPT-6.1-Sol xhigh Fast · ~/project/finance · ← for agents       ⚠ 2 warnings · f2 to view
`
	if !detectCodexTurnDone(doneScreen) {
		t.Fatalf("expected detectCodexTurnDone=true for finished Codex screen")
	}

	// 2. Active Turn 2 while Turn 1's "Worked for" is still visible above.
	workingScreen := `
  Worked for 3m 41s • 15:24

› [Image #1] 这个日志是为什么？

• Working (12s • esc to interrupt)

› Ask Codex to do anything
  GPT-6.1-Sol xhigh Fast · ~/project/finance
`
	if detectCodexTurnDone(workingScreen) {
		t.Fatalf("expected detectCodexTurnDone=false while Turn 2 is actively working")
	}

	// 3. Even if spinner line is momentarily absent, a newer prompt/tool after Worked for must stay running.
	midTurnScreen := `
  Worked for 3m 41s • 15:24

› [Image #1] 这个日志是为什么？
• Explored
  └ Read UnionPayPreProcessInterceptor.java

› Ask Codex to do anything
  GPT-6.1-Sol xhigh Fast · ~/project/finance
`
	if detectCodexTurnDone(midTurnScreen) {
		t.Fatalf("expected detectCodexTurnDone=false when newer prompt/tool output follows Worked for")
	}
}
