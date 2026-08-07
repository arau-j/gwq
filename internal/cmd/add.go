package cmd

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/d-kuro/gwq/internal/duration"
	"github.com/d-kuro/gwq/internal/registry"
	"github.com/spf13/cobra"
)

var (
	addBranch      bool
	addInteractive bool
	addForce       bool
	addStay        bool
	addExpires     string
	addFrom        string
)

// addCmd represents the add command.
var addCmd = &cobra.Command{
	Use:   "add [branch] [path]",
	Short: "Create a new worktree",
	Long: `Create a new worktree for the specified branch.

If no path is provided, it will be generated based on the configuration template.
Use -i flag to interactively select a branch using fuzzy finder.`,
	Example: `  # Create worktree from existing branch
  gwq add feature/new-ui

  # Create at specific path
  gwq add feature/new-ui ~/projects/myapp-feature

  # Create new branch and worktree
  gwq add -b feature/api-v2

  # Interactive branch selection
  gwq add -i

  # Create worktree and stay in the directory
  gwq add -s feature/new-ui

  # Create worktree expiring in 7 days
  gwq add --expires 7d feature/experiment

  # Create worktree expiring in 1 hour
  gwq add --expires 1h hotfix/quick-test

  # Branch from a specific base instead of the remote's default branch
  gwq add -b feature/api-v2 --from origin/release-2

  # Branch from the current HEAD (the pre-0.1.2 behaviour), e.g. to stack
  # a branch on top of the work in the worktree you are standing in
  gwq add -b feature/api-v2-followup --from HEAD`,
	RunE:              runAdd,
	ValidArgsFunction: getBranchCompletions,
}

func init() {
	rootCmd.AddCommand(addCmd)

	addCmd.Flags().BoolVarP(&addBranch, "branch", "b", false, "Create new branch")
	addCmd.Flags().BoolVarP(&addInteractive, "interactive", "i", false, "Select branch using fuzzy finder")
	addCmd.Flags().BoolVarP(&addForce, "force", "f", false, "Overwrite existing directory")
	addCmd.Flags().BoolVarP(&addStay, "stay", "s", false, "Stay in worktree directory after creation")
	addCmd.Flags().StringVar(&addExpires, "expires", "", "Set expiration (e.g., 1d, 7d, 1h)")
	addCmd.Flags().StringVar(&addFrom, "from", "",
		"Base ref for a branch created with -b (default: the remote's default branch; use HEAD for the current commit)")
}

func runAdd(cmd *cobra.Command, args []string) error {
	return ExecuteWithArgs(true, func(ctx *CommandContext, cmd *cobra.Command, args []string) error {
		var branch string
		var path string

		if addInteractive {
			if len(args) > 0 {
				return fmt.Errorf("cannot specify branch name with -i flag")
			}

			branches, err := ctx.Git.ListBranches(true)
			if err != nil {
				return fmt.Errorf("failed to list branches: %w", err)
			}

			selectedBranch, err := ctx.GetFinder().SelectBranch(branches)
			if err != nil {
				return fmt.Errorf("branch selection cancelled")
			}

			branch = selectedBranch.Name
			if selectedBranch.IsRemote {
				branch = selectedBranch.Name[len("origin/"):]
				addBranch = true
				// Picking origin/feature/x from the finder means "give me
				// that branch", so its own remote ref is the base — not the
				// default branch a bare -b would resolve to.
				if addFrom == "" {
					addFrom = selectedBranch.Name
				}
			}
		} else {
			if len(args) < 1 {
				return fmt.Errorf("branch name is required")
			}
			branch = args[0]
			if len(args) > 1 {
				path = args[1]
			}
		}

		if path != "" && !addForce {
			if err := ctx.WorktreeManager.ValidateWorktreePath(path); err != nil {
				return err
			}
		}

		// Validate --expires duration before creating the worktree so an
		// invalid value does not leave a stray worktree behind. The actual
		// ExpiresAt is computed after creation so the effective lifetime
		// isn't shortened by setup time (e.g. repository_settings hooks).
		var expiresDuration time.Duration
		if addExpires != "" {
			d, err := duration.Parse(addExpires)
			if err != nil {
				return fmt.Errorf("invalid --expires duration %q: %w", addExpires, err)
			}
			expiresDuration = d
		}

		var worktreePath string
		var err error
		var base baseChoice
		if addBranch {
			base = newBranchBase(ctx.Git, addFrom)
		}
		if base.Ref != "" {
			worktreePath, err = ctx.WorktreeManager.AddFromBase(branch, base.Ref, path)
		} else {
			worktreePath, err = ctx.WorktreeManager.Add(branch, path, addBranch)
		}
		if err != nil {
			return err
		}

		var expiresAt *time.Time
		if addExpires != "" {
			reg, err := registry.New()
			if err != nil {
				return fmt.Errorf("failed to open registry: %w", err)
			}

			repoURL, _ := ctx.Git.GetRepositoryURL()

			t := time.Now().Add(expiresDuration)
			expiresAt = &t

			entry := &registry.WorktreeEntry{
				Repository: repoURL,
				Branch:     branch,
				Path:       worktreePath,
				IsMain:     false,
				ExpiresAt:  expiresAt,
			}

			if err := reg.Register(entry); err != nil {
				return fmt.Errorf("failed to register worktree: %w", err)
			}
		}

		handleAddPostCreate(
			os.Stdout, os.Stderr,
			isCdShimActive(),
			ctx.Config.Cd.AutoCdOnAdd,
			addResult{
				Branch:    branch,
				Path:      worktreePath,
				Stay:      addStay,
				ExpiresAt: expiresAt,
				Base:      base,
			},
			LaunchShell,
		)
		return nil
	})(cmd, args)
}

// addResult carries the outcome of a successful `gwq add` into the
// post-create output routing.
type addResult struct {
	Branch    string
	Path      string
	Stay      bool
	ExpiresAt *time.Time
	Base      baseChoice
}

// baseChoice records what a new branch was started from, and how sure we are
// that it is current.
type baseChoice struct {
	// Ref is the start point passed to git. Empty means none was chosen and
	// git's own default applies — the current HEAD.
	Ref string
	// Defaulted reports that Ref came from the remote rather than from the
	// caller, which is the case worth printing: the caller did not say where
	// the branch should start, so tell them where it did.
	Defaulted bool
	// Stale reports that the ref could not be refreshed from the remote, so
	// the branch starts from whatever the last fetch left behind.
	Stale bool
}

// baseResolver is the git surface newBranchBase depends on.
type baseResolver interface {
	DefaultBaseRef(remote string) string
	FetchRef(ref string) error
}

// newBranchBase decides the start point for a branch created with -b.
//
// Without this, `git worktree add -b` starts the branch at the invoking
// process's HEAD. That is defensible in a single-checkout repository where you
// are usually standing on the trunk, and wrong under a bare repository where
// every checkout is a worktree and nobody ever stands on the trunk: the new
// branch inherits the commits of whichever worktree the shell happened to be
// in. Two pull requests opened on 2026-08-07 carried an unrelated commit for
// exactly this reason.
//
// `--from HEAD` is the escape hatch for the case the old default served —
// deliberately stacking a branch on the work in front of you.
func newBranchBase(g baseResolver, explicit string) baseChoice {
	if explicit == "HEAD" {
		return baseChoice{}
	}

	c := baseChoice{Ref: explicit}
	if c.Ref == "" {
		c.Ref = g.DefaultBaseRef("")
		c.Defaulted = true
		// No remote default to resolve — a local-only repository, or a clone
		// with neither main nor master. Keep git's behaviour rather than
		// failing a worktree creation over it.
		if c.Ref == "" {
			return baseChoice{}
		}
	}

	// A base is only worth defaulting to if it is current; branching from a
	// week-old trunk trades one surprise for another.
	if err := g.FetchRef(c.Ref); err != nil {
		c.Stale = true
	}
	return c
}

// handleAddPostCreate routes success messages and the worktree path to the
// appropriate destinations after a successful `gwq add`.
//
// Under shell integration (inShim), success messages go to stderr and stdout
// carries only the worktree path when a cd is wanted. When no cd is wanted,
// stdout is left empty — the shell wrapper's "-n" guard then refuses to cd
// into a success message.
func handleAddPostCreate(
	stdout, stderr io.Writer,
	inShim, autoCdOnAdd bool,
	r addResult,
	launchShell func(string) error,
) {
	wantCd := r.Stay || autoCdOnAdd

	msgDst := stdout
	if inShim {
		msgDst = stderr
	}
	_, _ = fmt.Fprintf(msgDst, "Created worktree for branch '%s'\n", r.Branch)
	// Announce a base the caller did not ask for. Silently choosing a start
	// point is how the old behaviour went unnoticed for so long.
	if r.Base.Defaulted && r.Base.Ref != "" {
		if r.Base.Stale {
			_, _ = fmt.Fprintf(msgDst,
				"Branched from %s (could not fetch — base may be stale)\n", r.Base.Ref)
		} else {
			_, _ = fmt.Fprintf(msgDst, "Branched from %s\n", r.Base.Ref)
		}
	}
	if r.ExpiresAt != nil {
		_, _ = fmt.Fprintf(msgDst, "Worktree expires at %s\n", r.ExpiresAt.Format(time.RFC3339))
	}

	switch {
	case inShim && wantCd:
		_, _ = fmt.Fprintln(stdout, r.Path)
	case inShim:
		// stdout intentionally empty: shell wrapper's `if [[ -n "$__gwq_result" ]]`
		// guard prevents an incorrect cd.
	case r.Stay:
		_ = launchShell(r.Path)
	}
}
