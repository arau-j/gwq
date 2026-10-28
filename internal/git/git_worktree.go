package git

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/d-kuro/gwq/pkg/models"
)

// ListWorktrees returns a list of all worktrees in the repository.
func (g *Git) ListWorktrees() ([]models.Worktree, error) {
	output, err := g.run("worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("failed to list worktrees: %w", err)
	}

	var worktrees []models.Worktree
	lines := strings.Split(strings.TrimSpace(output), "\n")

	for i := 0; i < len(lines); i++ {
		if after, ok := strings.CutPrefix(lines[i], "worktree "); ok {
			path := after

			var branch, commitHash string
			isMain := false

			for j := i + 1; j < len(lines) && !strings.HasPrefix(lines[j], "worktree "); j++ {
				if after, ok := strings.CutPrefix(lines[j], "branch "); ok {
					branch = after
					// Remove refs/heads/ prefix if present
					branch = strings.TrimPrefix(branch, "refs/heads/")
				} else if after, ok := strings.CutPrefix(lines[j], "HEAD "); ok {
					commitHash = after
				} else if strings.HasPrefix(lines[j], "bare") {
					continue
				}
				i = j
			}

			if branch == "" {
				branch = g.getCurrentBranch(path)
			}

			info, err := os.Stat(path)
			var createdAt time.Time
			if err == nil {
				createdAt = info.ModTime()
			}

			worktrees = append(worktrees, models.Worktree{
				Path:       path,
				Branch:     branch,
				CommitHash: commitHash,
				IsMain:     isMain,
				CreatedAt:  createdAt,
			})
		}
	}

	if len(worktrees) > 0 {
		mainDir, err := g.getMainRepoRoot()
		if err == nil {
			for i := range worktrees {
				resolvedPath := worktrees[i].Path
				if resolved, err := filepath.EvalSymlinks(resolvedPath); err == nil {
					resolvedPath = resolved
				}
				if resolvedPath == mainDir {
					worktrees[i].IsMain = true
					break
				}
			}
		}
	}

	return worktrees, nil
}

// AddWorktree creates a new worktree.
func (g *Git) AddWorktree(path, branch string, createBranch bool) error {
	args := []string{"worktree", "add"}

	if createBranch {
		args = append(args, "-b", branch, path)
	} else {
		args = append(args, path, branch)
	}

	if _, err := g.run(args...); err != nil {
		return fmt.Errorf("failed to add worktree: %w", err)
	}

	return nil
}

// AddWorktreeFromBase creates a new worktree with a branch from a specific base branch.
func (g *Git) AddWorktreeFromBase(path, branch, baseBranch string) error {
	args := []string{"worktree", "add", "-b", branch, path}

	if baseBranch != "" {
		args = append(args, baseBranch)
	}

	if _, err := g.run(args...); err != nil {
		return fmt.Errorf("failed to add worktree from base branch %s: %w", baseBranch, err)
	}

	return nil
}

// RemoveWorktree removes a worktree.
func (g *Git) RemoveWorktree(path string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)

	if _, err := g.run(args...); err != nil {
		if !force && mentionsSubmodules(err) {
			return fmt.Errorf("%w\n\n%s", err, submoduleRemoveHint)
		}
		return fmt.Errorf("failed to remove worktree: %w", err)
	}

	return nil
}

// git refuses to remove a worktree that contains submodules:
//
//	fatal: working trees containing submodules cannot be moved or removed
//
// It says so whether or not the worktree is clean, and it does not mention the
// flag that gets past it. `--force` does — measured on git 2.55.0, a plain
// remove of a submodule worktree fails and the same remove with `--force`
// succeeds — but nothing connects the two: gwq's own `--force` is documented as
// "Force delete even if dirty", which reads as being about uncommitted changes,
// so a caller staring at a submodule error has no reason to try it.
//
// An agent hit exactly that on 2026-08-19 and recorded the removal as
// impossible, leaving an empty worktree behind. So say it here, at the moment
// it is refused, along with the one thing `--force` genuinely risks.
const submoduleRemoveHint = "This worktree contains submodules, and git refuses to remove those without --force\n" +
	"even when the worktree is clean. Re-run with --force:\n" +
	"\n" +
	"    gwq remove --force <worktree>\n" +
	"\n" +
	"--force also discards uncommitted changes, so check `git -C <worktree> status` first\n" +
	"if the worktree might not be clean."

// mentionsSubmodules reports whether git refused because of submodules.
//
// Matched on the stable middle of the sentence rather than the whole line: git
// uses the same "containing submodules" wording for `worktree move` and has
// reworded around it before, and a hint that stops appearing after a git upgrade
// is worse than one that occasionally appears for the neighbouring verb.
func mentionsSubmodules(err error) bool {
	return strings.Contains(err.Error(), "containing submodules")
}

// PruneWorktrees removes worktree information for deleted directories.
func (g *Git) PruneWorktrees() error {
	if _, err := g.run("worktree", "prune"); err != nil {
		return fmt.Errorf("failed to prune worktrees: %w", err)
	}
	return nil
}
