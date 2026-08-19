package git

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMentionsSubmodulesMatchesGitsRefusal(t *testing.T) {
	// The exact line git 2.55.0 prints, captured from a real refusal rather
	// than typed from memory.
	real := errors.New("git worktree remove /wt: fatal: working trees containing submodules cannot be moved or removed\n")
	if !mentionsSubmodules(real) {
		t.Error("did not recognise git's own submodule refusal")
	}
	// Not every removal failure is this one, and a hint attached to an
	// unrelated error sends the reader somewhere useless.
	for _, other := range []string{
		"fatal: '/wt' contains modified or untracked files, use --force to delete it",
		"fatal: '/wt' is not a working tree",
		"fatal: validation failed, cannot remove working directory",
	} {
		if mentionsSubmodules(errors.New(other)) {
			t.Errorf("attached the submodule hint to an unrelated failure: %q", other)
		}
	}
}

// The behaviour the hint asserts. If a future git stops accepting --force here,
// the hint becomes wrong advice and this fails — which is the point: the whole
// fix rests on a claim about git, so the claim is tested against git.
//
// Skipped rather than failed where git or the submodule protocol is unavailable;
// this is an environment fact, not a defect in the code under test.
func TestForceRemovesAWorktreeContainingSubmodules(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()

	run := func(dir string, args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	mustRun := func(dir string, args ...string) {
		t.Helper()
		if out, err := run(dir, args...); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	sub := filepath.Join(root, "sub")
	mainRepo := filepath.Join(root, "main")
	wt := filepath.Join(root, "wt")
	for _, d := range []string{sub, mainRepo} {
		mustRun(root, "init", "-q", d)
		mustRun(d, "-c", "user.email=t@e", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")
	}

	// Local-path submodules need the file protocol enabled explicitly on
	// modern git; if the environment forbids it, there is nothing to test.
	if out, err := run(mainRepo, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "sub"); err != nil {
		t.Skipf("cannot add a local submodule here: %v\n%s", err, out)
	}
	mustRun(mainRepo, "-c", "user.email=t@e", "-c", "user.name=t", "commit", "-q", "-m", "add sub")
	mustRun(mainRepo, "worktree", "add", "-q", wt, "-b", "wtbranch")
	if out, err := run(wt, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "-q"); err != nil {
		t.Skipf("cannot init the submodule in the worktree: %v\n%s", err, out)
	}

	g := New(mainRepo)

	// Without force: git refuses, and gwq must now say what to do about it.
	err := g.RemoveWorktree(wt, false)
	if err == nil {
		t.Fatal("git accepted an unforced removal of a submodule worktree; the hint is obsolete")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("the refusal does not name the flag that gets past it:\n%v", err)
	}
	if !strings.Contains(err.Error(), "discards uncommitted changes") {
		t.Errorf("the hint recommends --force without naming what it risks:\n%v", err)
	}

	// With force: it succeeds. This is the claim the hint is built on.
	if err := g.RemoveWorktree(wt, true); err != nil {
		t.Fatalf("--force did not remove a submodule worktree, so the hint is wrong advice: %v", err)
	}
}
