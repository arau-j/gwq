package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitOut runs a git command in dir and returns its trimmed stdout.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit adds a file and commits it, returning nothing — the subject is what
// the assertions look for.
func commit(t *testing.T, dir, file, subject string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(subject+"\n"), 0644); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
	gitOut(t, dir, "add", ".")
	gitOut(t, dir, "commit", "-m", subject)
}

// cloneWithOrigin returns a clone of a fresh repository, so the clone has a
// real `origin` and real remote-tracking refs. DefaultBaseRef reads git state
// that only a genuine clone has (refs/remotes/origin/HEAD), so a hand-built
// fixture would test something other than what runs.
func cloneWithOrigin(t *testing.T) (upstream, clone string) {
	t.Helper()
	up := NewTestRepository(t)
	clone = filepath.Join(t.TempDir(), "clone")
	cmd := exec.Command("git", "clone", up.Path, clone)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	gitOut(t, clone, "config", "user.name", "Test User")
	gitOut(t, clone, "config", "user.email", "test@example.com")
	return up.Path, clone
}

func TestDefaultBaseRefResolvesTheRemoteDefaultBranch(t *testing.T) {
	_, clone := cloneWithOrigin(t)

	if got := New(clone).DefaultBaseRef(""); got != "origin/main" {
		t.Fatalf("DefaultBaseRef() = %q, want origin/main", got)
	}
}

// refs/remotes/origin/HEAD is written once at clone time and never updated. On
// 2026-08-07 a real bare repository's pointer named a long-abandoned feature
// branch while the remote's default had moved to main — trusting it would have
// branched every new worktree off the wrong trunk, which is the exact bug this
// code exists to prevent.
func TestDefaultBaseRefPrefersTheRemoteOverAStaleLocalPointer(t *testing.T) {
	_, clone := cloneWithOrigin(t)
	gitOut(t, clone, "branch", "abandoned", "origin/main")
	gitOut(t, clone, "push", "-q", "origin", "abandoned")
	gitOut(t, clone, "fetch", "-q", "origin")
	gitOut(t, clone, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/abandoned")

	if got := New(clone).DefaultBaseRef(""); got != "origin/main" {
		t.Fatalf("DefaultBaseRef() = %q, want origin/main from the remote, not the stale local pointer", got)
	}

	// And the stale pointer is repaired on the way past, for every other tool
	// that reads it.
	if got := gitOut(t, clone, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); got != "origin/main" {
		t.Fatalf("local origin/HEAD = %q, want it repaired to origin/main", got)
	}
}

// Offline is the common case on a laptop, and it must still produce a base.
// The local pointer is the best remaining evidence.
func TestDefaultBaseRefFallsBackToTheLocalPointerWhenTheRemoteIsUnreachable(t *testing.T) {
	_, clone := cloneWithOrigin(t)
	unreachable(t, clone)

	if got := New(clone).DefaultBaseRef(""); got != "origin/main" {
		t.Fatalf("DefaultBaseRef() = %q, want origin/main from the local pointer", got)
	}
}

// Offline *and* without refs/remotes/origin/HEAD — a clone made with an
// explicit refspec commonly lacks that pointer. The conventional names are the
// last resort before giving up.
func TestDefaultBaseRefFallsBackToConventionalNames(t *testing.T) {
	_, clone := cloneWithOrigin(t)
	unreachable(t, clone)
	gitOut(t, clone, "update-ref", "-d", "refs/remotes/origin/HEAD")

	if got := New(clone).DefaultBaseRef(""); got != "origin/main" {
		t.Fatalf("DefaultBaseRef() = %q, want origin/main from the name fallback", got)
	}
}

// unreachable repoints origin at a path that does not exist, so every network
// query fails fast without touching the network.
func unreachable(t *testing.T, dir string) {
	t.Helper()
	gitOut(t, dir, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
}

// A repository with no remote must yield no base, so the caller keeps git's own
// behaviour rather than failing worktree creation over a missing convention.
func TestDefaultBaseRefIsEmptyWithoutARemote(t *testing.T) {
	repo := NewTestRepository(t)

	if got := New(repo.Path).DefaultBaseRef(""); got != "" {
		t.Fatalf("DefaultBaseRef() = %q, want empty for a repository with no remote", got)
	}
}

func TestFetchRefUpdatesTheTrackingRef(t *testing.T) {
	upstream, clone := cloneWithOrigin(t)
	before := gitOut(t, clone, "rev-parse", "origin/main")
	commit(t, upstream, "new.txt", "upstream moved")

	if err := New(clone).FetchRef("origin/main"); err != nil {
		t.Fatalf("FetchRef: %v", err)
	}
	if after := gitOut(t, clone, "rev-parse", "origin/main"); after == before {
		t.Fatal("origin/main did not move; the base would have been stale")
	}
}

func TestFetchRefRejectsARefThatIsNotRemoteTracking(t *testing.T) {
	_, clone := cloneWithOrigin(t)

	if err := New(clone).FetchRef("main"); err == nil {
		t.Fatal("want an error for a local ref, got nil")
	}
}

// The bug this whole change exists to fix: a branch created while the process
// sits in a worktree that carries unmerged commits inherits them, and the pull
// request opened from it carries unrelated work.
func TestAddWorktreeFromBaseDoesNotInheritTheCurrentHEAD(t *testing.T) {
	_, clone := cloneWithOrigin(t)
	gitOut(t, clone, "checkout", "-b", "someones-feature")
	commit(t, clone, "unrelated.txt", "UNRELATED WORK")

	g := New(clone)
	fromBase := filepath.Join(t.TempDir(), "from-base")
	if err := g.AddWorktreeFromBase(fromBase, "clean", "origin/main"); err != nil {
		t.Fatalf("AddWorktreeFromBase: %v", err)
	}
	if log := gitOut(t, fromBase, "log", "--format=%s"); strings.Contains(log, "UNRELATED WORK") {
		t.Fatalf("new branch inherited the ambient HEAD:\n%s", log)
	}

	// Negative control: the path gwq took before this change does inherit it.
	// Without this the test above could pass for the wrong reason — e.g. if the
	// fixture never actually put the extra commit on HEAD.
	ambient := filepath.Join(t.TempDir(), "ambient")
	if err := g.AddWorktree(ambient, "inherits", true); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	if log := gitOut(t, ambient, "log", "--format=%s"); !strings.Contains(log, "UNRELATED WORK") {
		t.Fatalf("fixture is wrong: HEAD did not carry the unrelated commit:\n%s", log)
	}
}
