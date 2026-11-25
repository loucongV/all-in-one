package worktree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/loucongV/all-in-one/internal/protocol"
)

// InspectSubRepos scans up to 2 levels under root and returns all valid Git sub-repositories
// (with an existing HEAD commit), sorted deterministically by relative path.
func InspectSubRepos(root string) []protocol.SubRepoRef {
	found := FindSubRepos(root, 2)
	var out []protocol.SubRepoRef
	for _, subPath := range found {
		info, err := InspectRepo(subPath)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(root, subPath)
		if err != nil {
			continue
		}
		out = append(out, protocol.SubRepoRef{
			RelPath:    filepath.ToSlash(rel),
			RepoPath:   info.Root,
			BaseBranch: info.Branch,
			BaseCommit: info.HeadCommit,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RelPath < out[j].RelPath
	})
	return out
}

var skipMultiTopEntries = map[string]bool{
	".git":       true,
	".worktrees": true,
	".idea":      true,
	".vscode":    true,
}

// CreateMultiRepo creates a composite workspace directory mirroring rootDir:
//   - Every Git sub-repository gets a real isolated Git worktree on branchName
//   - Top-level non-Git files & directories (AGENTS.md, CLAUDE.md, docs/, config/, etc.)
//     are symlinked into the composite directory (zero disk duplication)
func CreateMultiRepo(rootDir string, subRepos []protocol.SubRepoRef, compositePath, branchName string) error {
	if len(subRepos) == 0 {
		return errors.New("no git sub-repositories found")
	}
	if err := os.MkdirAll(compositePath, 0o755); err != nil {
		return err
	}

	// Track which top-level dirs are sub-repos or ancestors of depth-2 sub-repos.
	exactSubRepo := make(map[string]protocol.SubRepoRef, len(subRepos))
	parentOfSubRepo := make(map[string]bool)
	for _, sr := range subRepos {
		exactSubRepo[sr.RelPath] = sr
		if parts := strings.Split(sr.RelPath, "/"); len(parts) > 1 {
			parentOfSubRepo[parts[0]] = true
		}
	}

	var createdSubs []protocol.SubRepoRef
	rollback := func() {
		for _, sr := range createdSubs {
			_ = Remove(sr.RepoPath, filepath.Join(compositePath, filepath.FromSlash(sr.RelPath)), branchName)
		}
		_ = os.RemoveAll(compositePath)
	}

	// 1. Create real Git worktrees for all sub-repositories.
	for _, sr := range subRepos {
		dst := filepath.Join(compositePath, filepath.FromSlash(sr.RelPath))
		if err := Create(sr.RepoPath, sr.BaseCommit, dst, branchName); err != nil {
			rollback()
			return fmt.Errorf("create sub-repo worktree %s: %w", sr.RelPath, err)
		}
		createdSubs = append(createdSubs, sr)
	}

	// 2. Symlink non-Git top-level files/dirs (CLAUDE.md, AGENTS.md, docs/, config/, etc.).
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		rollback()
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if skipMultiTopEntries[name] {
			continue
		}
		if _, isRepo := exactSubRepo[name]; isRepo {
			continue
		}
		if parentOfSubRepo[name] {
			// Symlink non-repo siblings inside a depth-1 parent directory that holds depth-2 sub-repos.
			subEntries, subErr := os.ReadDir(filepath.Join(rootDir, name))
			if subErr == nil {
				for _, se := range subEntries {
					rel := name + "/" + se.Name()
					if _, isRepo := exactSubRepo[rel]; isRepo || skipMultiTopEntries[se.Name()] {
						continue
					}
					src := filepath.Join(rootDir, name, se.Name())
					dst := filepath.Join(compositePath, name, se.Name())
					_ = os.MkdirAll(filepath.Dir(dst), 0o755)
					_ = os.Symlink(src, dst)
				}
			}
			continue
		}
		src := filepath.Join(rootDir, name)
		dst := filepath.Join(compositePath, name)
		_ = os.Symlink(src, dst)
	}

	return nil
}

// RemoveMultiRepo cleans up every sub-repo worktree & temporary branch, then removes the
// composite wrapper directory (os.RemoveAll unlinks symlinks without touching original targets).
func RemoveMultiRepo(subRepos []protocol.SubRepoRef, compositePath, branchName string) error {
	var errs []string
	for _, sr := range subRepos {
		wt := filepath.Join(compositePath, filepath.FromSlash(sr.RelPath))
		if err := Remove(sr.RepoPath, wt, branchName); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", sr.RelPath, err))
		}
	}
	if compositePath != "" {
		if err := os.RemoveAll(compositePath); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// DiffMultiRepo aggregates diffs across every sub-repo worktree inside compositePath relative to each sub-repo's BaseCommit.
func DiffMultiRepo(subRepos []protocol.SubRepoRef, compositePath string) (files []protocol.FileDiffStat, ins int, del int, diffText string, err error) {
	if _, statErr := os.Stat(compositePath); statErr != nil {
		return nil, 0, 0, "", fmt.Errorf("composite worktree missing: %w", statErr)
	}

	var buf strings.Builder
	for _, sr := range subRepos {
		wt := filepath.Join(compositePath, filepath.FromSlash(sr.RelPath))
		f, a, d, txt, dErr := DiffWorktree(wt, sr.BaseCommit)
		if dErr != nil {
			continue
		}
		if len(f) == 0 && strings.TrimSpace(txt) == "" {
			continue
		}
		for _, fs := range f {
			fs.Path = filepath.ToSlash(filepath.Join(sr.RelPath, fs.Path))
			files = append(files, fs)
		}
		ins += a
		del += d
		buf.WriteString(fmt.Sprintf("========== [%s] (%s) +%d/-%d ==========\n", sr.RelPath, sr.BaseBranch, a, d))
		buf.WriteString(txt)
		if !strings.HasSuffix(txt, "\n") {
			buf.WriteByte('\n')
		}
		buf.WriteByte('\n')
	}

	return files, ins, del, buf.String(), nil
}

// AdoptMultiRepo merges only the modified sub-repositories from winnerCompositePath back into
// each sub-repo's BaseBranch using GitHub's strategy (squash | merge | rebase), then removes
// all composite worktrees and temporary branches.
func AdoptMultiRepo(subRepos []protocol.SubRepoRef, winnerCompositePath, winnerBranch string, strategy protocol.MergeStrategy, commitMsg string, allTargets []RunCleanupTarget) (cleaned int, mergedRepos []string, warnings []string, err error) {
	if strategy == "" {
		strategy = protocol.MergeSquash
	}

	// 1. Identify which sub-repositories actually have changes in winnerCompositePath.
	var changedSubs []protocol.SubRepoRef
	for _, sr := range subRepos {
		wt := filepath.Join(winnerCompositePath, filepath.FromSlash(sr.RelPath))
		f, _, _, txt, dErr := DiffWorktree(wt, sr.BaseCommit)
		if dErr != nil {
			continue
		}
		if len(f) > 0 || strings.TrimSpace(txt) != "" {
			changedSubs = append(changedSubs, sr)
		}
	}

	// 2. Pre-flight cleanliness check ONLY on sub-repos that will be merged.
	for _, sr := range changedSubs {
		st, stErr := runGit(sr.RepoPath, "status", "--porcelain")
		if stErr != nil {
			return 0, nil, nil, fmt.Errorf("inspect %s: %w", sr.RelPath, stErr)
		}
		if st != "" {
			return 0, nil, nil, fmt.Errorf("子仓库 %s 主目录存在未提交改动，请先 commit 或 stash 后再采纳", sr.RelPath)
		}
	}

	// 3. Merge each modified sub-repo back to its BaseBranch.
	for _, sr := range changedSubs {
		wt := filepath.Join(winnerCompositePath, filepath.FromSlash(sr.RelPath))
		msg := commitMsg
		if msg == "" {
			msg = fmt.Sprintf("aio: adopt %s (%s)", winnerBranch, sr.RelPath)
		}
		if _, _, mergeErr := Adopt(sr.RepoPath, sr.BaseBranch, wt, winnerBranch, strategy, msg, nil); mergeErr != nil {
			return 0, mergedRepos, nil, fmt.Errorf("合并子仓库 %s 失败: %w", sr.RelPath, mergeErr)
		}
		mergedRepos = append(mergedRepos, sr.RelPath)
	}

	// 4. Clean up all composite worktrees & temporary branches across all runs in the task.
	for _, target := range allTargets {
		if rmErr := RemoveMultiRepo(subRepos, target.WorktreePath, target.Branch); rmErr != nil {
			warnings = append(warnings, rmErr.Error())
		} else {
			cleaned++
		}
	}
	return cleaned, mergedRepos, warnings, nil
}
