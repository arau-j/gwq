package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/d-kuro/gwq/internal/duration"
	"github.com/d-kuro/gwq/internal/registry"
	"github.com/spf13/cobra"
)

var renewExpires string

// renewCmd extends an existing worktree's expiry.
//
// `gwq add --expires` sets a lifetime once, at creation. Nothing could ever
// change it afterwards, and that makes the expiry wrong for the one case that
// matters most: a worktree that is REUSED. Tooling that resumes work in an
// existing checkout (Hive's launch path does exactly this — it reuses a worktree
// when one already exists for the branch) inherits the original deadline, so a
// worktree created on day 1 and resumed on day 15 is expired *while someone is
// working in it*. The tree is clean at that moment, so every safety check in
// `prune --expired` agrees it is fine to delete.
//
// Renewal is how a caller says "this is still in use", and it is the reason the
// sweep can be safely automated at all.
var renewCmd = &cobra.Command{
	Use:   "renew [branch]",
	Short: "Extend an existing worktree's expiration",
	Long: `Extend the expiration of an existing worktree.

Use this when a worktree is reused or resumed: its original --expires deadline
does not know that work restarted, so without renewal an in-use worktree can
expire and be reclaimed by 'gwq prune --expired'.`,
	Example: `  # Give a reused worktree another 14 days
  gwq renew feature/api-v2 --expires 14d

  # Renew the worktree you are standing in
  gwq renew --expires 7d`,
	Args:              cobra.MaximumNArgs(1),
	RunE:              runRenew,
	ValidArgsFunction: getBranchCompletions,
}

func init() {
	rootCmd.AddCommand(renewCmd)
	renewCmd.Flags().StringVar(&renewExpires, "expires", "", "New lifetime from now (e.g. 1d, 7d, 1h)")
	_ = renewCmd.MarkFlagRequired("expires")
}

func runRenew(cmd *cobra.Command, args []string) error {
	return ExecuteWithArgs(true, func(ctx *CommandContext, cmd *cobra.Command, args []string) error {
		d, err := duration.Parse(renewExpires)
		if err != nil {
			return fmt.Errorf("invalid --expires duration %q: %w", renewExpires, err)
		}

		path, branch, err := renewTarget(ctx, args)
		if err != nil {
			return err
		}

		reg, err := registry.New()
		if err != nil {
			return fmt.Errorf("failed to open registry: %w", err)
		}
		repoURL, _ := ctx.Git.GetRepositoryURL()

		// Register overwrites by path, so this is an update for a worktree
		// already known and a first registration for one created before
		// --expires existed. Both are wanted: the second is how an existing
		// fleet of unregistered worktrees becomes reclaimable at all.
		at := time.Now().Add(d)
		if err := reg.Register(&registry.WorktreeEntry{
			Repository: repoURL,
			Branch:     branch,
			Path:       path,
			IsMain:     false,
			ExpiresAt:  &at,
		}); err != nil {
			return fmt.Errorf("failed to register worktree: %w", err)
		}

		fmt.Printf("Worktree %s expires at %s\n", path, at.Format(time.RFC3339))
		return nil
	})(cmd, args)
}

// renewTarget resolves which worktree to renew: the named branch, or the one
// the caller is standing in.
func renewTarget(ctx *CommandContext, args []string) (path, branch string, err error) {
	if len(args) == 0 {
		wts, lerr := ctx.WorktreeManager.List()
		if lerr != nil {
			return "", "", fmt.Errorf("failed to list worktrees: %w", lerr)
		}
		cwd, cerr := os.Getwd()
		if cerr != nil {
			return "", "", cerr
		}
		// Resolve both sides: a worktree path reached through a symlink (a
		// /tmp that is really /private/tmp, a stow'd home) would not match the
		// registry's own spelling, and the renewal would silently register a
		// second entry for a path that prune never sees.
		cwd, _ = filepath.EvalSymlinks(cwd)
		for _, wt := range wts {
			p, _ := filepath.EvalSymlinks(wt.Path)
			if p == cwd {
				return wt.Path, wt.Branch, nil
			}
		}
		return "", "", fmt.Errorf("not inside a worktree; name one: gwq renew <branch> --expires <dur>")
	}
	branch = args[0]
	p, gerr := ctx.WorktreeManager.GetWorktreePath(branch)
	if gerr != nil {
		return "", "", fmt.Errorf("no worktree for %q: %w", branch, gerr)
	}
	return p, branch, nil
}
