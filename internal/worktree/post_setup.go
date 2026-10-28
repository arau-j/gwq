package worktree

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/d-kuro/gwq/internal/command"
	"github.com/d-kuro/gwq/internal/filesystem"
	"github.com/d-kuro/gwq/internal/template"
	"github.com/d-kuro/gwq/internal/url"
	"github.com/d-kuro/gwq/internal/utils"
	"github.com/d-kuro/gwq/pkg/models"
)

// runPostWorktreeSetup runs file copy and setup commands for the new worktree.
// branch is used as the raw value for {{.Branch}} in templated setup commands.
func (m *Manager) runPostWorktreeSetup(branch, worktreePath string) {
	m.runPostWorktreeSetupWithExecutor(context.Background(), command.NewStandardExecutor(), branch, worktreePath)
}

// runPostWorktreeSetupWithExecutor is the test seam for runPostWorktreeSetup.
// It returns the SetupResult slice so tests can assert on per-command outcomes.
func (m *Manager) runPostWorktreeSetupWithExecutor(ctx context.Context, executor Executor, branch, worktreePath string) []SetupResult {
	if len(m.config.RepositorySettings) == 0 {
		return nil
	}

	repoRoot, err := m.git.GetMainRepositoryPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[gwq] warning: failed to get repository path: %v\n", err)
		return nil
	}

	repoSetting := findRepoSetting(m.config.RepositorySettings, repoRoot)
	if repoSetting == nil {
		// Most repositories legitimately have no entry, and saying so every time
		// would be noise. A configured path that does NOT EXIST is different:
		// it can never match anything, so its setup_commands are dead and
		// nothing says so.
		//
		// That is not hypothetical. `repository = "/home/joan/repos/pi-house"`
		// went stale when that repo moved to a bare layout on 2026-08-05, and
		// for two weeks every worktree cut from it started with no
		// `node_modules` — so the repo's own prescribed gate answered
		// `tsc: command not found`. Three sessions hit it and none could see
		// why, because a skipped setup produces no output at all. The same
		// class recurred five days later for the same repo's clones under
		// ~/.hive/scratch.
		//
		// Reported only on the miss, so a matched repo pays nothing, and only
		// for literal paths — a glob that matches nothing today may match
		// tomorrow and is not evidence of rot.
		if stale := staleRepositoryPaths(m.config.RepositorySettings); len(stale) > 0 {
			fmt.Fprintf(os.Stderr,
				"[gwq] warning: no repository_settings matched %s, so no setup ran. "+
					"These configured paths do not exist and can never match: %s\n",
				repoRoot, strings.Join(stale, ", "))
		}
		return nil
	}

	for _, err := range CopyFilesWithGlob(filesystem.NewStandardFileSystem(), repoRoot, worktreePath, repoSetting.CopyFiles) {
		fmt.Fprintf(os.Stderr, "[gwq] file copy error: %v\n", err)
	}

	data := buildSetupTemplateData(m.git, branch, worktreePath)
	rendered := template.RenderCommands(repoSetting.SetupCommands, data)

	toRun := make([]string, 0, len(rendered))
	for _, rc := range rendered {
		if rc.Err != nil {
			fmt.Fprintf(os.Stderr, "[gwq] setup command template error: %v\n", rc.Err)
			continue
		}
		toRun = append(toRun, rc.Rendered)
	}

	results := RunSetupCommands(ctx, executor, worktreePath, toRun)
	for _, r := range results {
		if r.Output != "" {
			fmt.Fprintf(os.Stderr, "[gwq] setup command output: %s\n", r.Output)
		}
		if r.Err != nil {
			fmt.Fprintf(os.Stderr, "[gwq] setup command error: %s: %v\n", r.Command, r.Err)
		}
	}

	return results
}

// buildSetupTemplateData assembles the data for rendering setup commands.
// When the repository has no resolvable origin URL, Host/Owner/Repository/Hash
// are left empty and a warning is logged — commands that only reference
// {{.Branch}} / {{.Path}} still work.
func buildSetupTemplateData(git GitInterface, branch, worktreePath string) *template.TemplateData {
	data := &template.TemplateData{
		Branch: branch,
		Path:   worktreePath,
	}

	repoURL, err := git.GetRepositoryURL()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[gwq] warning: origin URL unavailable, Host/Owner/Repository/Hash will be empty: %v\n", err)
		return data
	}

	repoInfo, err := url.ParseRepositoryURL(repoURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[gwq] warning: failed to parse repository URL %q: %v\n", repoURL, err)
		return data
	}

	data.Host = repoInfo.Host
	data.Owner = repoInfo.Owner
	data.Repository = repoInfo.Repository
	data.Hash = template.ShortHash(repoInfo.FullPath + "/" + branch)
	return data
}

// findRepoSetting returns the first matching RepositorySetting for the given repo root.
func findRepoSetting(settings []models.RepositorySetting, repoRoot string) *models.RepositorySetting {
	for i, s := range settings {
		if utils.MatchPath(s.Repository, repoRoot) {
			return &settings[i]
		}
	}
	return nil
}

// staleRepositoryPaths reports configured `repository` values that name a path
// which is not there.
//
// Literal paths only. A glob is a pattern for repositories that may not exist
// yet — `~/.hive/scratch/*/pi-house` names none until an agent is launched —
// so an unmatched glob is ordinary and an unmatched literal is a typo or a
// layout change nobody propagated here.
//
// A leading `~` is caught by this too, and deliberately: gwq does not expand
// it, so `~/repos/foo` is a literal directory name that will never exist and
// its setup_commands are as dead as a stale absolute path's.
func staleRepositoryPaths(settings []models.RepositorySetting) []string {
	var stale []string
	for _, s := range settings {
		if s.Repository == "" || strings.ContainsAny(s.Repository, "*?[") {
			continue
		}
		if _, err := os.Stat(s.Repository); err != nil && os.IsNotExist(err) {
			stale = append(stale, s.Repository)
		}
	}
	return stale
}
