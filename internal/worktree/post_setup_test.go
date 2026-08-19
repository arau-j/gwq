package worktree

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/d-kuro/gwq/pkg/models"
)

// recordingExecutor records every Execute call so we can assert the exact
// rendered command string passed to `sh -c`.
type recordingExecutor struct {
	fakeExecutor
}

func newRecordingExecutor() *recordingExecutor {
	return &recordingExecutor{}
}

func (r *recordingExecutor) rendered() []string {
	out := make([]string, 0, len(r.calls))
	for _, c := range r.calls {
		// args layout is always ["-c", "<rendered cmd>"]
		if len(c.args) == 2 && c.args[0] == "-c" {
			out = append(out, c.args[1])
		}
	}
	return out
}

func buildManagerWithRepoSetting(g *mockGit, setting models.RepositorySetting) *Manager {
	cfg := &models.Config{
		RepositorySettings: []models.RepositorySetting{setting},
	}
	return &Manager{git: g, config: cfg}
}

func TestRunPostWorktreeSetup_RendersTemplateVariables(t *testing.T) {
	git := &mockGit{
		repoPath: "/mock/repo/path",
		repoURL:  "https://github.com/test-user/test-repo.git",
	}
	setting := models.RepositorySetting{
		Repository: "/mock/repo/path",
		SetupCommands: []string{
			"echo branch={{.Branch}} path={{.Path}}",
			"echo host={{.Host}} owner={{.Owner}} repo={{.Repository}}",
		},
	}
	m := buildManagerWithRepoSetting(git, setting)

	exec := newRecordingExecutor()
	results := m.runPostWorktreeSetupWithExecutor(context.Background(), exec, "feature/new-ui", "/tmp/worktrees/gwq/feature-new-ui")

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	got := exec.rendered()
	wantContains := []string{
		"branch=feature/new-ui path=/tmp/worktrees/gwq/feature-new-ui",
		"host=github.com owner=test-user repo=test-repo",
	}
	for i, w := range wantContains {
		if i >= len(got) {
			t.Fatalf("missing rendered command at index %d", i)
		}
		if !strings.Contains(got[i], w) {
			t.Errorf("rendered[%d] = %q; want it to contain %q", i, got[i], w)
		}
	}
}

func TestRunPostWorktreeSetup_FallbackWhenRemoteUnavailable(t *testing.T) {
	git := &mockGit{
		repoPath:     "/mock/repo/path",
		repoURLError: errors.New("no origin remote"),
	}
	setting := models.RepositorySetting{
		Repository: "/mock/repo/path",
		SetupCommands: []string{
			"echo branch={{.Branch}} path={{.Path}} host=[{{.Host}}]",
		},
	}
	m := buildManagerWithRepoSetting(git, setting)

	exec := newRecordingExecutor()
	results := m.runPostWorktreeSetupWithExecutor(context.Background(), exec, "topic", "/wt/topic")

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	got := exec.rendered()
	if len(got) != 1 {
		t.Fatalf("expected 1 rendered call, got %d", len(got))
	}
	if !strings.Contains(got[0], "branch=topic") {
		t.Errorf("rendered = %q; missing branch=topic", got[0])
	}
	if !strings.Contains(got[0], "path=/wt/topic") {
		t.Errorf("rendered = %q; missing path=/wt/topic", got[0])
	}
	if !strings.Contains(got[0], "host=[]") {
		t.Errorf("rendered = %q; Host should be empty when remote is unavailable", got[0])
	}
}

func TestRunPostWorktreeSetup_TemplateErrorSkipsOnlyFailing(t *testing.T) {
	git := &mockGit{
		repoPath: "/mock/repo/path",
		repoURL:  "https://github.com/test-user/test-repo.git",
	}
	setting := models.RepositorySetting{
		Repository: "/mock/repo/path",
		SetupCommands: []string{
			"echo ok {{.Branch}}",
			"echo bad {{.NoSuchVar}}",
			"echo also ok {{.Path}}",
		},
	}
	m := buildManagerWithRepoSetting(git, setting)

	exec := newRecordingExecutor()
	results := m.runPostWorktreeSetupWithExecutor(context.Background(), exec, "br", "/wt/br")

	if len(results) != 2 {
		t.Fatalf("expected 2 results (bad skipped), got %d", len(results))
	}
	got := exec.rendered()
	if len(got) != 2 {
		t.Fatalf("expected 2 executor calls, got %d", len(got))
	}
	if !strings.Contains(got[0], "ok br") {
		t.Errorf("rendered[0] = %q; want contains \"ok br\"", got[0])
	}
	if !strings.Contains(got[1], "also ok /wt/br") {
		t.Errorf("rendered[1] = %q; want contains \"also ok /wt/br\"", got[1])
	}
}

func TestRunPostWorktreeSetup_NoMatchingRepoSetting(t *testing.T) {
	git := &mockGit{repoPath: "/mock/repo/path"}
	setting := models.RepositorySetting{
		Repository:    "/different/repo",
		SetupCommands: []string{"echo should-not-run"},
	}
	m := buildManagerWithRepoSetting(git, setting)

	exec := newRecordingExecutor()
	results := m.runPostWorktreeSetupWithExecutor(context.Background(), exec, "br", "/wt/br")

	if len(results) != 0 {
		t.Errorf("expected no results when repo does not match, got %d", len(results))
	}
	if len(exec.calls) != 0 {
		t.Errorf("expected no executor calls, got %d", len(exec.calls))
	}
}

// A configured path that does not exist can never match anything, so its
// setup_commands are dead — and a skipped setup produces no output at all.
//
// `repository = "/home/joan/repos/pi-house"` went stale when that repo moved to
// a bare layout on 2026-08-05. For two weeks every worktree cut from it started
// with no dependencies, so the repo's own prescribed gate answered
// `tsc: command not found`. Three sessions hit it and none could see why. The
// same class recurred five days later for that repo's clones under a scratch
// root. One line at match time would have shown both.
func TestStaleRepositoryPaths(t *testing.T) {
	existing := t.TempDir()

	got := staleRepositoryPaths([]models.RepositorySetting{
		{Repository: existing},
		{Repository: filepath.Join(existing, "gone")},
		// A glob names repositories that may not exist YET — a scratch root has
		// none until an agent is launched — so an unmatched glob is ordinary and
		// must never be reported as rot.
		{Repository: filepath.Join(existing, "*", "pi-house")},
		{Repository: ""},
	})

	want := []string{filepath.Join(existing, "gone")}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("staleRepositoryPaths = %v; want %v", got, want)
	}
}

// The WIRING: the warning has to reach stderr on the miss. A helper that
// computes a perfect list nobody prints is the silent skip again.
func TestRunPostWorktreeSetup_WarnsAboutStaleConfiguredPaths(t *testing.T) {
	git := &mockGit{repoPath: "/mock/repo/path"}
	m := buildManagerWithRepoSetting(git, models.RepositorySetting{
		Repository:    "/definitely/not/here",
		SetupCommands: []string{"echo should-not-run"},
	})

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	m.runPostWorktreeSetupWithExecutor(context.Background(), newRecordingExecutor(), "br", "/wt/br")
	os.Stderr = orig
	_ = w.Close()
	out, _ := io.ReadAll(r)

	for _, want := range []string{"/definitely/not/here", "no setup ran", "/mock/repo/path"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("stderr = %q; want it to contain %q", out, want)
		}
	}
}

// …and a repo that simply has no entry stays quiet. Most repositories are that
// case, and a warning on every one of them is noise that trains the reader to
// skip the line the stale case needs them to read.
func TestRunPostWorktreeSetup_QuietWhenNothingIsStale(t *testing.T) {
	existing := t.TempDir()
	git := &mockGit{repoPath: "/mock/repo/path"}
	m := buildManagerWithRepoSetting(git, models.RepositorySetting{
		Repository:    existing,
		SetupCommands: []string{"echo should-not-run"},
	})

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	m.runPostWorktreeSetupWithExecutor(context.Background(), newRecordingExecutor(), "br", "/wt/br")
	os.Stderr = orig
	_ = w.Close()
	out, _ := io.ReadAll(r)

	if len(out) != 0 {
		t.Errorf("stderr = %q; want silence when the config is merely unmatched", out)
	}
}
