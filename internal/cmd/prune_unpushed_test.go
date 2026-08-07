package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Real git, because the question is what git reports about remote-tracking refs
// — something Go cannot typecheck and a stub would only restate.
func pruneGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "maintenance.auto=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// pruneRepo returns a clone whose HEAD is present on origin.
func pruneRepo(t *testing.T) string {
	t.Helper()
	up := filepath.Join(t.TempDir(), "up")
	pruneGit(t, t.TempDir(), "init", "-q", "-b", "main", up)
	pruneGit(t, up, "commit", "-q", "--allow-empty", "-m", "trunk")

	clone := filepath.Join(t.TempDir(), "clone")
	pruneGit(t, t.TempDir(), "clone", "-q", up, clone)
	return clone
}

// The dirty check misses this entirely: a clean tree can still be the only
// checkout of work nobody else has seen. Before this guard existed, an expired
// worktree holding a fresh commit was removed without a word.
func TestHasUnpushedCommitsSeesWorkNoRemoteHas(t *testing.T) {
	repo := pruneRepo(t)

	// Everything is on origin at this point.
	if unpushed, err := hasUnpushedCommits(repo); err != nil || unpushed {
		t.Fatalf("fresh clone = %v, %v; want not unpushed", unpushed, err)
	}

	pruneGit(t, repo, "commit", "-q", "--allow-empty", "-m", "local work")
	unpushed, err := hasUnpushedCommits(repo)
	if err != nil {
		t.Fatalf("hasUnpushedCommits: %v", err)
	}
	if !unpushed {
		t.Fatal("a commit no remote has is unpushed work")
	}
}

// The check asks "does any remote-tracking ref contain HEAD", NOT "is there an
// upstream". These branches routinely have no upstream configured — tooling
// pushes them without setting tracking — and an upstream-based check would call
// such a branch unpushed forever, making prune a permanent no-op.
func TestHasUnpushedCommitsIgnoresMissingUpstreamConfig(t *testing.T) {
	repo := pruneRepo(t)
	pruneGit(t, repo, "checkout", "-q", "-b", "agents/x")
	pruneGit(t, repo, "commit", "-q", "--allow-empty", "-m", "work")
	// Push WITHOUT -u, so the branch exists on the remote with no tracking
	// config — exactly what the agent lanes produce.
	pruneGit(t, repo, "push", "-q", "origin", "agents/x")
	pruneGit(t, repo, "fetch", "-q", "origin")

	if upstream := runQuietGit(t, repo, "rev-parse", "--abbrev-ref", "agents/x@{upstream}"); upstream != "" {
		t.Fatalf("fixture has an upstream (%q); it must not, or this proves nothing", upstream)
	}
	if unpushed, err := hasUnpushedCommits(repo); err != nil || unpushed {
		t.Fatalf("pushed-without-tracking = %v, %v; want not unpushed", unpushed, err)
	}
}

// runQuietGit returns "" instead of failing, for probing state that is allowed
// to be absent.
func runQuietGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
