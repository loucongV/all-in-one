package worktree

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/loucongV/all-in-one/internal/protocol"
)

type ProjectConfig struct {
	Worktree struct {
		Copy    []string `toml:"copy"`
		Symlink []string `toml:"symlink"`
		Setup   []string `toml:"setup"`
	} `toml:"worktree"`
}

type RepoInfo struct {
	Root       string
	Branch     string
	HeadCommit string
	Dirty      bool
	IsGitRepo  bool
}

// InspectWorkspace validates that dir is an existing local directory.
// If it is a Git repository with at least one commit, Git metadata is populated (IsGitRepo=true).
// Plain directories and empty Git repositories are also accepted (IsGitRepo=false).
func InspectWorkspace(dir string) (RepoInfo, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return RepoInfo{}, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return RepoInfo{}, fmt.Errorf("directory not accessible: %w", err)
	}
	if !fi.IsDir() {
		return RepoInfo{}, fmt.Errorf("path is not a directory: %s", abs)
	}

	if gitInfo, err := InspectRepo(abs); err == nil {
		gitInfo.IsGitRepo = true
		return gitInfo, nil
	}

	return RepoInfo{
		Root:      abs,
		IsGitRepo: false,
	}, nil
}

func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return "", fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// InspectRepo validates that dir is inside a Git repository and returns its root, branch, and HEAD SHA.
func InspectRepo(dir string) (RepoInfo, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return RepoInfo{}, err
	}
	root, err := runGit(abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return RepoInfo{}, fmt.Errorf("not a git repository: %w", err)
	}
	commit, err := runGit(root, "rev-parse", "HEAD")
	if err != nil {
		return RepoInfo{}, fmt.Errorf("repository has no commits yet (HEAD missing): %w", err)
	}
	branch, err := runGit(root, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		branch = "HEAD"
	}
	statusOut, err := runGit(root, "status", "--porcelain")
	if err != nil {
		return RepoInfo{}, err
	}
	return RepoInfo{
		Root:       root,
		Branch:     branch,
		HeadCommit: commit,
		Dirty:      statusOut != "",
	}, nil
}

// Create creates an isolated worktree branching from baseCommit and applies optional .aio.toml rules.
func Create(repoPath, baseCommit, worktreePath, branchName string) error {
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return err
	}
	// Clean stale path if it exists from an interrupted run.
	if _, err := os.Stat(worktreePath); err == nil {
		_, _ = runGit(repoPath, "worktree", "remove", "--force", worktreePath)
		_ = os.RemoveAll(worktreePath)
	}
	_, _ = runGit(repoPath, "worktree", "prune")

	if _, err := runGit(repoPath, "worktree", "add", "-b", branchName, worktreePath, baseCommit); err != nil {
		return err
	}

	if err := applyProjectConfig(repoPath, worktreePath); err != nil {
		return fmt.Errorf("apply .aio.toml: %w", err)
	}
	return nil
}

func applyProjectConfig(repoPath, worktreePath string) error {
	cfgPath := filepath.Join(repoPath, ".aio.toml")
	data, err := os.ReadFile(cfgPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var cfg ProjectConfig
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return err
	}

	for _, rel := range cfg.Worktree.Copy {
		src := filepath.Join(repoPath, rel)
		dst := filepath.Join(worktreePath, rel)
		if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := copyPath(src, dst); err != nil {
			return fmt.Errorf("copy %s: %w", rel, err)
		}
	}

	for _, rel := range cfg.Worktree.Symlink {
		src := filepath.Join(repoPath, rel)
		dst := filepath.Join(worktreePath, rel)
		if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		_ = os.RemoveAll(dst)
		if err := os.Symlink(src, dst); err != nil {
			return fmt.Errorf("symlink %s: %w", rel, err)
		}
	}

	for _, script := range cfg.Worktree.Setup {
		cmd := exec.Command("sh", "-c", script)
		cmd.Dir = worktreePath
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("setup %q failed: %w (%s)", script, err, strings.TrimSpace(string(out)))
		}
	}

	return nil
}

func copyPath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return filepath.Walk(src, func(p string, fi os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(src, p)
			if err != nil {
				return err
			}
			target := filepath.Join(dst, rel)
			if fi.IsDir() {
				return os.MkdirAll(target, fi.Mode())
			}
			return copyFile(p, target, fi.Mode())
		})
	}
	return copyFile(src, dst, info.Mode())
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// Remove deletes a git worktree and its temporary branch.
func Remove(repoPath, worktreePath, branchName string) error {
	var errs []string
	if worktreePath != "" {
		if _, err := runGit(repoPath, "worktree", "remove", "--force", worktreePath); err != nil {
			if rmErr := os.RemoveAll(worktreePath); rmErr != nil {
				errs = append(errs, err.Error())
			}
		}
	}
	_, _ = runGit(repoPath, "worktree", "prune")
	if branchName != "" {
		if _, err := runGit(repoPath, "branch", "-D", branchName); err != nil {
			// Ignore if branch was already deleted.
			if !strings.Contains(err.Error(), "not found") {
				errs = append(errs, err.Error())
			}
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// DiffWorktree computes non-destructive diff stats & unified diff relative to baseCommit,
// including committed, staged, unstaged, and untracked files.
func DiffWorktree(worktreePath, baseCommit string) (files []protocol.FileDiffStat, ins int, del int, diffText string, err error) {
	if _, statErr := os.Stat(worktreePath); statErr != nil {
		return nil, 0, 0, "", fmt.Errorf("worktree missing: %w", statErr)
	}

	numstat, err := runGit(worktreePath, "diff", "--numstat", baseCommit)
	if err != nil {
		return nil, 0, 0, "", err
	}
	if numstat != "" {
		for _, line := range strings.Split(numstat, "\n") {
			parts := strings.SplitN(line, "\t", 3)
			if len(parts) != 3 {
				continue
			}
			a, _ := strconv.Atoi(parts[0])
			d, _ := strconv.Atoi(parts[1])
			ins += a
			del += d
			files = append(files, protocol.FileDiffStat{
				Path:    parts[2],
				Added:   a,
				Deleted: d,
			})
		}
	}

	var diffBuf strings.Builder
	trackedDiff, err := runGit(worktreePath, "diff", baseCommit)
	if err != nil {
		return nil, 0, 0, "", err
	}
	if trackedDiff != "" {
		diffBuf.WriteString(trackedDiff)
		diffBuf.WriteByte('\n')
	}

	untrackedOut, err := runGit(worktreePath, "ls-files", "--others", "--exclude-standard")
	if err == nil && untrackedOut != "" {
		for _, rel := range strings.Split(untrackedOut, "\n") {
			rel = strings.TrimSpace(rel)
			if rel == "" {
				continue
			}
			full := filepath.Join(worktreePath, rel)
			content, rErr := os.ReadFile(full)
			lineCount := 0
			if rErr == nil {
				if len(content) > 0 {
					lineCount = bytes.Count(content, []byte{'\n'})
					if content[len(content)-1] != '\n' {
						lineCount++
					}
				}
				diffBuf.WriteString(fmt.Sprintf("diff --git a/%s b/%s\nnew file\n--- /dev/null\n+++ b/%s\n", rel, rel, rel))
				for _, l := range strings.Split(strings.TrimRight(string(content), "\n"), "\n") {
					diffBuf.WriteString("+" + l + "\n")
				}
			}
			ins += lineCount
			files = append(files, protocol.FileDiffStat{
				Path:      rel,
				Added:     lineCount,
				Untracked: true,
			})
		}
	}

	return files, ins, del, diffBuf.String(), nil
}

var skipScanDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	"target":       true,
	"dist":         true,
	"build":        true,
	".venv":        true,
	"venv":         true,
	"__pycache__":  true,
	".idea":        true,
	".vscode":      true,
}

// FindSubRepos discovers nested Git repositories under root up to maxDepth levels deep.
// Once a directory containing .git is found, it does not descend further into that repo.
func FindSubRepos(root string, maxDepth int) []string {
	var repos []string
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > maxDepth {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".") || skipScanDirs[name] {
				continue
			}
			full := filepath.Join(dir, name)
			if !e.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(full, ".git")); err == nil {
				repos = append(repos, full)
				continue
			}
			if depth < maxDepth {
				walk(full, depth+1)
			}
		}
	}
	walk(root, 1)
	return repos
}

// DiffLocalWorkspace collects live uncommitted changes in a local workspace.
// If dir itself is a Git repo, it diffs against HEAD.
// Otherwise it scans nested Git sub-repositories (e.g. multi-repo folders like erp/)
// and aggregates all sub-repos that have local changes.
func DiffLocalWorkspace(dir string) (files []protocol.FileDiffStat, ins int, del int, diffText string, repoCount int, err error) {
	if _, statErr := os.Stat(dir); statErr != nil {
		return nil, 0, 0, "", 0, fmt.Errorf("workspace missing: %w", statErr)
	}

	if info, gitErr := InspectRepo(dir); gitErr == nil {
		f, a, d, txt, dErr := DiffWorktree(info.Root, "HEAD")
		return f, a, d, txt, 1, dErr
	}

	subRepos := FindSubRepos(dir, 2)
	if len(subRepos) == 0 {
		return nil, 0, 0, "", 0, nil
	}

	var buf strings.Builder
	for _, sub := range subRepos {
		info, gitErr := InspectRepo(sub)
		if gitErr != nil {
			continue
		}
		f, a, d, txt, dErr := DiffWorktree(info.Root, "HEAD")
		if dErr != nil || (len(f) == 0 && strings.TrimSpace(txt) == "") {
			continue
		}
		relRepo, relErr := filepath.Rel(dir, sub)
		if relErr != nil {
			relRepo = filepath.Base(sub)
		}
		for _, fs := range f {
			fs.Path = filepath.ToSlash(filepath.Join(relRepo, fs.Path))
			files = append(files, fs)
		}
		ins += a
		del += d
		buf.WriteString(fmt.Sprintf("========== [%s] (%s) +%d/-%d ==========\n", filepath.ToSlash(relRepo), info.Branch, a, d))
		buf.WriteString(txt)
		if !strings.HasSuffix(txt, "\n") {
			buf.WriteByte('\n')
		}
		buf.WriteByte('\n')
	}

	return files, ins, del, buf.String(), len(subRepos), nil
}

type RunCleanupTarget struct {
	WorktreePath string
	Branch       string
}

// Adopt commits any uncommitted work in winnerWorktree, merges winnerBranch into baseBranch
// using GitHub's standard strategy (squash | merge | rebase), and cleans up all worktrees/branches.
func Adopt(repoPath, baseBranch, winnerWorktree, winnerBranch string, strategy protocol.MergeStrategy, commitMsg string, allTargets []RunCleanupTarget) (cleaned int, warnings []string, err error) {
	if strategy == "" {
		strategy = protocol.MergeSquash
	}
	if commitMsg == "" {
		commitMsg = fmt.Sprintf("aio: adopt %s", winnerBranch)
	}

	// Ensure main repo is clean before modifying it.
	statusOut, err := runGit(repoPath, "status", "--porcelain")
	if err != nil {
		return 0, nil, err
	}
	if statusOut != "" {
		return 0, nil, errors.New("main repository has uncommitted changes; please commit or stash before adopting")
	}

	// Commit any dirty changes inside winner worktree so nothing is lost.
	wtStatus, err := runGit(winnerWorktree, "status", "--porcelain")
	if err != nil {
		return 0, nil, err
	}
	if wtStatus != "" {
		if _, err := runGit(winnerWorktree, "add", "-A"); err != nil {
			return 0, nil, err
		}
		if _, err := runGit(winnerWorktree, "commit", "-m", commitMsg); err != nil {
			return 0, nil, err
		}
	}

	// Ensure main repo is on baseBranch.
	if baseBranch != "" && baseBranch != "HEAD" {
		if _, err := runGit(repoPath, "checkout", baseBranch); err != nil {
			return 0, nil, fmt.Errorf("checkout %s: %w", baseBranch, err)
		}
	}

	switch strategy {
	case protocol.MergeSquash:
		if _, err := runGit(repoPath, "merge", "--squash", winnerBranch); err != nil {
			_, _ = runGit(repoPath, "reset", "--merge")
			return 0, nil, fmt.Errorf("squash merge conflict: %w", err)
		}
		staged, _ := runGit(repoPath, "diff", "--cached", "--name-only")
		if staged != "" {
			if _, err := runGit(repoPath, "commit", "-m", commitMsg); err != nil {
				return 0, nil, fmt.Errorf("squash commit failed: %w", err)
			}
		}

	case protocol.MergeCommit:
		if _, err := runGit(repoPath, "merge", "--no-ff", winnerBranch, "-m", commitMsg); err != nil {
			_, _ = runGit(repoPath, "merge", "--abort")
			return 0, nil, fmt.Errorf("merge conflict: %w", err)
		}

	case protocol.MergeRebase:
		if _, err := runGit(winnerWorktree, "rebase", baseBranch); err != nil {
			_, _ = runGit(winnerWorktree, "rebase", "--abort")
			return 0, nil, fmt.Errorf("rebase conflict: %w", err)
		}
		if _, err := runGit(repoPath, "merge", "--ff-only", winnerBranch); err != nil {
			return 0, nil, fmt.Errorf("fast-forward after rebase failed: %w", err)
		}

	default:
		return 0, nil, fmt.Errorf("unsupported merge strategy: %s", strategy)
	}

	// Clean up all worktrees & temporary branches in the task.
	for _, t := range allTargets {
		if rmErr := Remove(repoPath, t.WorktreePath, t.Branch); rmErr != nil {
			warnings = append(warnings, rmErr.Error())
		} else {
			cleaned++
		}
	}
	return cleaned, warnings, nil
}
