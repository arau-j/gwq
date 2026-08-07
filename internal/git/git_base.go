package git

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// DefaultRemote is the remote consulted when resolving a default base ref.
const DefaultRemote = "origin"

// remoteQueryTimeout bounds the one network call DefaultBaseRef makes. A slow
// or hanging remote must degrade to the local answer, never stall a worktree.
const remoteQueryTimeout = 10 * time.Second

// DefaultBaseRef reports the ref a newly created branch should start from when
// the caller did not name one: the remote's default branch, e.g. "origin/main".
//
// `git worktree add -b <branch> <path>` with no start point branches from
// whatever HEAD the invoking process happens to be sitting on. Under a bare
// repository with every checkout a worktree, nobody is ever "on main" — so a
// worktree created while the shell sat in another worktree silently inherited
// that worktree's unmerged commits, and the pull request opened from it carried
// unrelated work.
//
// Returns "" (with no error) when no default can be established, which lets the
// caller keep git's own behaviour rather than inventing a base.
func (g *Git) DefaultBaseRef(remote string) string {
	if remote == "" {
		remote = DefaultRemote
	}

	// Ask the remote. refs/remotes/<remote>/HEAD is written once at clone time
	// and never updated, so it goes stale the moment a repository's default
	// branch is renamed or repointed — measured on 2026-08-07, one bare
	// repository's local pointer named a feature branch while the remote's
	// default had long since moved to main. Branching every new worktree from
	// a stale trunk is precisely the bug this function exists to prevent, so
	// the authoritative answer is worth one round trip.
	if name := g.remoteHeadBranch(remote); name != "" {
		ref := remote + "/" + name
		g.repairLocalRemoteHead(remote, ref)
		return ref
	}

	// Offline, or a remote that does not answer: the local pointer is the best
	// remaining evidence, and is usually right.
	if out, err := g.run("symbolic-ref", "--short", "refs/remotes/"+remote+"/HEAD"); err == nil {
		if ref := strings.TrimSpace(out); ref != "" {
			return ref
		}
	}

	// Some clones have no refs/remotes/<remote>/HEAD at all. Fall back to the
	// two conventional names before giving up.
	for _, name := range []string{"main", "master"} {
		ref := remote + "/" + name
		if _, err := g.run("rev-parse", "--verify", "--quiet", ref); err == nil {
			return ref
		}
	}

	return ""
}

// remoteHeadBranch asks the remote which branch its HEAD points at, returning
// the short branch name (e.g. "main") or "" when the remote cannot be reached.
func (g *Git) remoteHeadBranch(remote string) string {
	ctx, cancel := context.WithTimeout(context.Background(), remoteQueryTimeout)
	defer cancel()

	out, err := g.runWithContext(ctx, "ls-remote", "--symref", remote, "HEAD")
	if err != nil {
		return ""
	}
	// The symref line is "ref: refs/heads/main\tHEAD", followed by the usual
	// "<sha>\tHEAD" line.
	for line := range strings.SplitSeq(out, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ref: refs/heads/")
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(rest, "\t")
		return strings.TrimSpace(name)
	}
	return ""
}

// repairLocalRemoteHead points refs/remotes/<remote>/HEAD at the branch the
// remote actually calls default.
//
// Best effort and deliberately silent: it is a courtesy to every other tool
// that reads that pointer, not something DefaultBaseRef's own answer depends
// on. A repository whose objects for the branch are not fetched yet will fail
// here, and that is fine — the caller fetches next.
func (g *Git) repairLocalRemoteHead(remote, ref string) {
	current, err := g.run("symbolic-ref", "--short", "refs/remotes/"+remote+"/HEAD")
	if err == nil && strings.TrimSpace(current) == ref {
		return
	}
	_, _ = g.run("symbolic-ref", "refs/remotes/"+remote+"/HEAD", "refs/remotes/"+ref)
}

// FetchRef updates a single remote-tracking ref.
//
// Best effort by contract: an offline machine or an unreachable remote must not
// stop a worktree from being created, it only means the base is as fresh as the
// last fetch. The error is returned so a caller can report the staleness rather
// than silently branching from a week-old trunk.
func (g *Git) FetchRef(remoteRef string) error {
	remote, branch, ok := strings.Cut(remoteRef, "/")
	if !ok || remote == "" || branch == "" {
		return fmt.Errorf("not a remote-tracking ref: %q", remoteRef)
	}
	if _, err := g.run("fetch", "--quiet", remote, branch); err != nil {
		return err
	}
	return nil
}
