package adapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/loucongV/all-in-one/internal/protocol"
)

func TestDefaultRegistryAndLaunchSpecs(t *testing.T) {
	reg := DefaultRegistry()
	infos := reg.ListInfo()
	if len(infos) != 6 {
		t.Fatalf("expected 6 adapters, got %d", len(infos))
	}

	spec := LaunchSpec{
		WorkDir:   "/tmp/wt-test",
		Prompt:    "implement feature X",
		Model:     "test-model",
		PermMode:  protocol.PermAuto,
		RunID:     "run-123",
		SockPath:  "/tmp/aio.sock",
		SelfBin:   "/usr/local/bin/aio",
		SessionID: "11111111-2222-4333-8444-555555555555",
	}

	// Claude: inline --settings JSON hooks + --session-id binding + positional prompt + acceptEdits
	claudeAdp, err := reg.Get("claude")
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := claudeAdp.BuildLaunch(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cmd.Args, "--permission-mode") || !slices.Contains(cmd.Args, "acceptEdits") {
		t.Fatalf("claude missing acceptEdits: %v", cmd.Args)
	}
	if !slices.Contains(cmd.Args, "--session-id") || !slices.Contains(cmd.Args, spec.SessionID) {
		t.Fatalf("claude missing --session-id binding: %v", cmd.Args)
	}
	settingsIdx := slices.Index(cmd.Args, "--settings")
	if settingsIdx == -1 || settingsIdx+1 >= len(cmd.Args) {
		t.Fatalf("claude missing --settings hook injection: %v", cmd.Args)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(cmd.Args[settingsIdx+1]), &parsed); err != nil {
		t.Fatalf("claude --settings invalid JSON: %v", err)
	}
	if cmd.Args[len(cmd.Args)-1] != spec.Prompt {
		t.Fatalf("claude prompt not positional last arg: %v", cmd.Args)
	}

	// Codex: -C dir, -c notify=..., --approve-for-me, positional prompt
	codexAdp, err := reg.Get("codex")
	if err != nil {
		t.Fatal(err)
	}
	cmd, err = codexAdp.BuildLaunch(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cmd.Args, "--no-alt-screen") || !slices.Contains(cmd.Args, "-C") || !slices.Contains(cmd.Args, "--approve-for-me") {
		t.Fatalf("codex args mismatch: %v", cmd.Args)
	}
	hasNotify := false
	for _, a := range cmd.Args {
		if strings.HasPrefix(a, "notify=") && strings.Contains(a, "run-123") {
			hasNotify = true
		}
	}
	if !hasNotify {
		t.Fatalf("codex missing notify hook override: %v", cmd.Args)
	}

	// Antigravity: -i prompt, --mode accept-edits, session-only --settings title state hook
	agyAdp, err := reg.Get("agy")
	if err != nil {
		t.Fatal(err)
	}
	cmd, err = agyAdp.BuildLaunch(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cmd.Args, "-i") || !slices.Contains(cmd.Args, "accept-edits") {
		t.Fatalf("agy args mismatch: %v", cmd.Args)
	}
	agySettingsIdx := slices.Index(cmd.Args, "--settings")
	if agySettingsIdx == -1 || agySettingsIdx+1 >= len(cmd.Args) {
		t.Fatalf("agy missing --settings title state hook: %v", cmd.Args)
	}
	var agySettings map[string]any
	if err := json.Unmarshal([]byte(cmd.Args[agySettingsIdx+1]), &agySettings); err != nil {
		t.Fatalf("agy --settings invalid JSON: %v", err)
	}
	titleCfg, _ := agySettings["title"].(map[string]any)
	titleCmd, _ := titleCfg["command"].(string)
	if !strings.Contains(titleCmd, "--agy-state") || !strings.Contains(titleCmd, "run-123") {
		t.Fatalf("agy --settings missing --agy-state hook command: %v", agySettings)
	}
	agyResume, err := agyAdp.BuildResume(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(agyResume.Args, "--settings") {
		t.Fatalf("agy resume missing --settings title state hook: %v", agyResume.Args)
	}

	// Cursor: --trust, --workspace, --auto-review, positional prompt
	curAdp, err := reg.Get("cursor")
	if err != nil {
		t.Fatal(err)
	}
	cmd, err = curAdp.BuildLaunch(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cmd.Args, "--trust") || !slices.Contains(cmd.Args, "--auto-review") || !slices.Contains(cmd.Args, "--workspace") {
		t.Fatalf("cursor args mismatch: %v", cmd.Args)
	}

	// Verify BuildResume modes (picker / id / fresh / last) preserve permissions.
	codexPicker, _ := codexAdp.BuildResume(LaunchSpec{WorkDir: "/tmp/erp", PermMode: protocol.PermYolo, ResumeMode: "picker"})
	if !slices.Contains(codexPicker.Args, "resume") || slices.Contains(codexPicker.Args, "--last") || !slices.Contains(codexPicker.Args, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("codex picker resume mismatch: %v", codexPicker.Args)
	}
	codexID, _ := codexAdp.BuildResume(LaunchSpec{WorkDir: "/tmp/erp", ResumeMode: "id", SessionID: "01a029f1"})
	if !slices.Contains(codexID.Args, "--no-alt-screen") || !slices.Contains(codexID.Args, "resume") || !slices.Contains(codexID.Args, "01a029f1") {
		t.Fatalf("codex id resume mismatch: %v", codexID.Args)
	}
	claudePicker, _ := claudeAdp.BuildResume(LaunchSpec{WorkDir: "/tmp/erp", ResumeMode: "picker"})
	if !slices.Contains(claudePicker.Args, "--resume") {
		t.Fatalf("claude picker resume mismatch: %v", claudePicker.Args)
	}
	curPicker, _ := curAdp.BuildResume(LaunchSpec{WorkDir: "/tmp/erp", ResumeMode: "picker"})
	if !slices.Contains(curPicker.Args, "--resume") {
		t.Fatalf("cursor picker resume mismatch: %v", curPicker.Args)
	}

	// Kimi Code: --yolo (auto), --auto (yolo), --plan (plan), --continue / --session
	kimiAdp, err := reg.Get("kimi")
	if err != nil {
		t.Fatal(err)
	}
	kimiLaunch, err := kimiAdp.BuildLaunch(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(kimiLaunch.Args, "--yolo") || kimiLaunch.Dir != spec.WorkDir {
		t.Fatalf("kimi launch args mismatch: %+v", kimiLaunch)
	}
	kimiYolo, _ := kimiAdp.BuildLaunch(LaunchSpec{WorkDir: "/tmp/erp", PermMode: protocol.PermYolo})
	if !slices.Contains(kimiYolo.Args, "--auto") {
		t.Fatalf("kimi yolo args mismatch: %v", kimiYolo.Args)
	}
	kimiResumeID, _ := kimiAdp.BuildResume(LaunchSpec{WorkDir: "/tmp/erp", ResumeMode: "id", SessionID: "sess-abc"})
	if !slices.Contains(kimiResumeID.Args, "--session") || !slices.Contains(kimiResumeID.Args, "sess-abc") {
		t.Fatalf("kimi resume id mismatch: %v", kimiResumeID.Args)
	}

	// OpenCode: --model, --prompt, --continue / --session, positional dir
	ocAdp, err := reg.Get("opencode")
	if err != nil {
		t.Fatal(err)
	}
	ocLaunch, err := ocAdp.BuildLaunch(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ocLaunch.Args, "--prompt") || !slices.Contains(ocLaunch.Args, spec.Prompt) || ocLaunch.Args[len(ocLaunch.Args)-1] != spec.WorkDir {
		t.Fatalf("opencode launch args mismatch: %v", ocLaunch.Args)
	}
	ocResumeLast, _ := ocAdp.BuildResume(LaunchSpec{WorkDir: "/tmp/erp", ResumeMode: "last"})
	if !slices.Contains(ocResumeLast.Args, "--continue") {
		t.Fatalf("opencode resume last mismatch: %v", ocResumeLast.Args)
	}
}

func TestBinaryResolverPriorityAndMagpieWrap(t *testing.T) {
	tmp := t.TempDir()
	binA := filepath.Join(tmp, "custom-kimi")
	if err := os.WriteFile(binA, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	nonExec := filepath.Join(tmp, "not-exec")
	if err := os.WriteFile(nonExec, []byte("text"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewBinaryResolver()
	r.SetExtraDirs([]string{tmp})
	if p, ok := r.Resolve("kimi", "custom-kimi"); !ok || p != binA {
		t.Fatalf("expected extra dir resolution to find %s, got (%q, %v)", binA, p, ok)
	}
	// Non-executable file must be rejected.
	r.SetOverride("kimi", nonExec)
	if p, ok := r.Resolve("kimi", "definitely-nonexistent-bin-xyz"); ok {
		t.Fatalf("non-executable override should not resolve, got %q", p)
	}
	// Valid executable override wins.
	r.SetOverride("kimi", binA)
	if p, ok := r.Resolve("kimi", "definitely-nonexistent-bin-xyz"); !ok || p != binA {
		t.Fatalf("expected override %s, got (%q, %v)", binA, p, ok)
	}

	// Magpie wrapping preserves env/dir and passes agent args after "--".
	orig := CommandSpec{
		Binary: "/usr/bin/claude",
		Args:   []string{"--session-id", "123"},
		Env:    []string{"AIO_RUN_ID=run-1"},
		Dir:    "/tmp/repo",
	}
	wrapped := WrapWithMagpie("/usr/local/bin/magpie", "claude", "deepseek/deepseek-v4-pro", orig)
	if wrapped.Binary != "/usr/local/bin/magpie" {
		t.Fatalf("expected magpie binary, got %q", wrapped.Binary)
	}
	wantArgs := []string{"claude", "deepseek/deepseek-v4-pro", "--", "--session-id", "123"}
	if !slices.Equal(wrapped.Args, wantArgs) {
		t.Fatalf("magpie wrapped args = %v, want %v", wrapped.Args, wantArgs)
	}
	if wrapped.Dir != orig.Dir || !slices.Equal(wrapped.Env, orig.Env) {
		t.Fatalf("magpie wrap lost Dir or Env: %+v", wrapped)
	}
	// Unsupported agent stays unchanged.
	unchanged := WrapWithMagpie("/usr/local/bin/magpie", "cursor", "deepseek/deepseek-v4-pro", orig)
	if unchanged.Binary != orig.Binary {
		t.Fatalf("unsupported agent should not be wrapped by magpie")
	}
}
