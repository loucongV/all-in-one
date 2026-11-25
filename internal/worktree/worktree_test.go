package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/loucongV/all-in-one/internal/protocol"
)

func initTestRepo(t *testing.T) (string, RepoInfo) {
	t.Helper()
	dir := t.TempDir()
	mustGit := func(args ...string) {
		t.Helper()
		if _, err := runGit(dir, args...); err != nil {
			t.Fatalf("git %v failed: %v", args, err)
		}
	}
	mustGit("init", "-b", "main")
	mustGit("config", "user.email", "test@aio.local")
	mustGit("config", "user.name", "AIO Test")

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("SECRET=123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".aio.toml"), []byte("[worktree]\ncopy = [\".env\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit("add", "README.md", ".gitignore", ".aio.toml")
	mustGit("commit", "-m", "initial commit")

	info, err := InspectRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir, info
}

func TestWorktreeCreateDiffAndAdoptStrategies(t *testing.T) {
	strategies := []protocol.MergeStrategy{
		protocol.MergeSquash,
		protocol.MergeCommit,
		protocol.MergeRebase,
	}

	for _, strat := range strategies {
		t.Run(string(strat), func(t *testing.T) {
			repo, info := initTestRepo(t)
			wtRoot := t.TempDir()

			wtWinner := filepath.Join(wtRoot, "winner")
			brWinner := "aio/cmp/claude"
			wtLoser := filepath.Join(wtRoot, "loser")
			brLoser := "aio/cmp/codex"

			if err := Create(repo, info.HeadCommit, wtWinner, brWinner); err != nil {
				t.Fatalf("create winner worktree: %v", err)
			}
			if err := Create(repo, info.HeadCommit, wtLoser, brLoser); err != nil {
				t.Fatalf("create loser worktree: %v", err)
			}

			// Verify .aio.toml copied .env into isolated worktree.
			envBytes, err := os.ReadFile(filepath.Join(wtWinner, ".env"))
			if err != nil || strings.TrimSpace(string(envBytes)) != "SECRET=123" {
				t.Fatalf("expected .env copied into worktree, got %q err=%v", string(envBytes), err)
			}

			// Make tracked + untracked changes in winner worktree (uncommitted!).
			if err := os.WriteFile(filepath.Join(wtWinner, "README.md"), []byte("hello\nfrom winner\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(wtWinner, "newfile.txt"), []byte("line1\nline2\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			// Make competing change in loser worktree.
			if err := os.WriteFile(filepath.Join(wtLoser, "README.md"), []byte("hello\nfrom loser\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			// Verify DiffWorktree picks up both modified tracked file and untracked file.
			files, ins, del, diffText, err := DiffWorktree(wtWinner, info.HeadCommit)
			if err != nil {
				t.Fatalf("DiffWorktree failed: %v", err)
			}
			if len(files) != 2 || ins != 3 || del != 0 {
				t.Fatalf("unexpected diff stats: files=%+v ins=%d del=%d", files, ins, del)
			}
			if !strings.Contains(diffText, "+from winner") || !strings.Contains(diffText, "+line1") {
				t.Fatalf("diff text missing expected hunks:\n%s", diffText)
			}

			// Adopt winner and verify cleanup of BOTH worktrees & branches.
			cleaned, warnings, err := Adopt(repo, info.Branch, wtWinner, brWinner, strat, "adopt winner "+string(strat), []RunCleanupTarget{
				{WorktreePath: wtWinner, Branch: brWinner},
				{WorktreePath: wtLoser, Branch: brLoser},
			})
			if err != nil {
				t.Fatalf("Adopt(%s) failed: %v", strat, err)
			}
			if cleaned != 2 || len(warnings) != 0 {
				t.Fatalf("expected 2 cleaned with 0 warnings, got cleaned=%d warnings=%v", cleaned, warnings)
			}

			// Main repo must now have winner's changes.
			readme, err := os.ReadFile(filepath.Join(repo, "README.md"))
			if err != nil || !strings.Contains(string(readme), "from winner") {
				t.Fatalf("winner README not merged into main: %q err=%v", string(readme), err)
			}
			newf, err := os.ReadFile(filepath.Join(repo, "newfile.txt"))
			if err != nil || !strings.Contains(string(newf), "line2") {
				t.Fatalf("winner newfile not merged into main: %q err=%v", string(newf), err)
			}

			// Both worktree paths and temporary branches must be gone.
			if _, err := os.Stat(wtWinner); !os.IsNotExist(err) {
				t.Fatalf("winner worktree was not removed")
			}
			if _, err := os.Stat(wtLoser); !os.IsNotExist(err) {
				t.Fatalf("loser worktree was not removed")
			}
			branches, _ := runGit(repo, "branch", "--list", "aio/*")
			if strings.TrimSpace(branches) != "" {
				t.Fatalf("temporary branches were not cleaned up: %s", branches)
			}
		})
	}
}

func TestDiffLocalWorkspaceAggregatesMultiSubRepos(t *testing.T) {
	parent := t.TempDir()

	// Create 2 nested git repos (one dirty, one clean) under a plain parent workspace (like erp/).
	makeSubRepo := func(name string) string {
		dir := filepath.Join(parent, name)
		_ = os.MkdirAll(dir, 0o755)
		_, _ = runGit(dir, "init", "-b", "main")
		_, _ = runGit(dir, "config", "user.email", "test@aio.local")
		_, _ = runGit(dir, "config", "user.name", "AIO Test")
		_ = os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)
		_, _ = runGit(dir, "add", "main.go")
		_, _ = runGit(dir, "commit", "-m", "init")
		return dir
	}

	svcA := makeSubRepo("service-a")
	_ = makeSubRepo("service-b")

	// Modify tracked file and add untracked file in service-a only.
	_ = os.WriteFile(filepath.Join(svcA, "main.go"), []byte("package main\n// updated\n"), 0o644)
	_ = os.WriteFile(filepath.Join(svcA, "extra.go"), []byte("package main\n"), 0o644)

	files, ins, del, diffText, repoCount, err := DiffLocalWorkspace(parent)
	if err != nil {
		t.Fatalf("DiffLocalWorkspace failed: %v", err)
	}
	if repoCount != 2 {
		t.Fatalf("expected 2 discovered sub-repos, got %d", repoCount)
	}
	if len(files) != 2 || ins != 2 || del != 0 {
		t.Fatalf("unexpected aggregated stats: files=%+v ins=%d del=%d", files, ins, del)
	}
	if !strings.Contains(diffText, "[service-a]") || !strings.Contains(diffText, "+// updated") {
		t.Fatalf("expected [service-a] header and diff hunk, got:\n%s", diffText)
	}
	if strings.Contains(diffText, "[service-b]") {
		t.Fatalf("clean sub-repo service-b should not appear in diff output:\n%s", diffText)
	}
}
