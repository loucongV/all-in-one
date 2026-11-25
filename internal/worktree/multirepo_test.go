package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/loucongV/all-in-one/internal/protocol"
)

func TestMultiRepoCompositeWorktreeLifecycle(t *testing.T) {
	root := t.TempDir()

	// Create top-level shared docs (like workspace/CLAUDE.md and workspace/docs/).
	_ = os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("# Workspace Rules\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(root, "docs"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "docs", "arch.md"), []byte("arch\n"), 0o644)

	makeSub := func(name string) string {
		dir := filepath.Join(root, name)
		_ = os.MkdirAll(dir, 0o755)
		_, _ = runGit(dir, "init", "-b", "main")
		_, _ = runGit(dir, "config", "user.email", "test@aio.local")
		_, _ = runGit(dir, "config", "user.name", "AIO Test")
		_ = os.WriteFile(filepath.Join(dir, "app.go"), []byte("package app\n"), 0o644)
		_, _ = runGit(dir, "add", "app.go")
		_, _ = runGit(dir, "commit", "-m", "init")
		return dir
	}

	svcA := makeSub("service-api")
	svcB := makeSub("service-web")

	subs := InspectSubRepos(root)
	if len(subs) != 2 {
		t.Fatalf("expected 2 sub-repos, got %d: %+v", len(subs), subs)
	}

	wtRoot := t.TempDir()
	compDir := filepath.Join(wtRoot, "ws-task1")
	branch := "aio/task1"

	if err := CreateMultiRepo(root, subs, compDir, branch); err != nil {
		t.Fatalf("CreateMultiRepo failed: %v", err)
	}

	// Verify top-level CLAUDE.md and docs/ are accessible via symlink.
	if data, err := os.ReadFile(filepath.Join(compDir, "CLAUDE.md")); err != nil || !strings.Contains(string(data), "Workspace Rules") {
		t.Fatalf("expected symlinked CLAUDE.md readable, got %q err=%v", string(data), err)
	}

	// Modify ONLY service-api in the composite worktree; leave service-web clean (and even make original service-web dirty!).
	_ = os.WriteFile(filepath.Join(compDir, "service-api", "app.go"), []byte("package app\n// edited by agent\n"), 0o644)
	_ = os.WriteFile(filepath.Join(svcB, "local-wip.txt"), []byte("user editing locally\n"), 0o644)

	// Verify DiffMultiRepo reports only service-api.
	files, ins, del, diffText, err := DiffMultiRepo(subs, compDir)
	if err != nil {
		t.Fatalf("DiffMultiRepo failed: %v", err)
	}
	if len(files) != 1 || ins != 1 || del != 0 || files[0].Path != "service-api/app.go" {
		t.Fatalf("unexpected multi-repo diff: files=%+v ins=%d del=%d", files, ins, del)
	}
	if !strings.Contains(diffText, "[service-api]") {
		t.Fatalf("expected [service-api] header in diff:\n%s", diffText)
	}

	// Adopt with Squash: should succeed EVEN THOUGH untouched service-web is dirty in original root!
	cleaned, merged, warnings, err := AdoptMultiRepo(subs, compDir, branch, protocol.MergeSquash, "adopt multi-repo", []RunCleanupTarget{
		{WorktreePath: compDir, Branch: branch},
	})
	if err != nil {
		t.Fatalf("AdoptMultiRepo failed: %v", err)
	}
	if cleaned != 1 || len(warnings) != 0 || len(merged) != 1 || merged[0] != "service-api" {
		t.Fatalf("unexpected adopt result: cleaned=%d merged=%v warnings=%v", cleaned, merged, warnings)
	}

	// Original service-api must have agent's change; original CLAUDE.md and service-web WIP must be untouched!
	gotA, _ := os.ReadFile(filepath.Join(svcA, "app.go"))
	if !strings.Contains(string(gotA), "// edited by agent") {
		t.Fatalf("merged change missing in service-api: %q", string(gotA))
	}
	if _, err := os.Stat(filepath.Join(root, "CLAUDE.md")); err != nil {
		t.Fatalf("original CLAUDE.md must not be affected by composite removal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(svcB, "local-wip.txt")); err != nil {
		t.Fatalf("original service-web WIP must remain untouched: %v", err)
	}
	if _, err := os.Stat(compDir); !os.IsNotExist(err) {
		t.Fatalf("composite worktree dir should be removed")
	}
}
