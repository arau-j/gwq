package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestHandleAddPostCreate(t *testing.T) {
	t.Parallel()

	expAt := time.Now().Add(24 * time.Hour)

	tests := []struct {
		name           string
		inShim         bool
		autoCdOnAdd    bool
		stay           bool
		expiresAt      *time.Time
		wantStdout     string
		wantStderrHas  []string
		wantShellCalls int
	}{
		{
			name:           "shim+stay: path on stdout, msg on stderr",
			inShim:         true,
			stay:           true,
			wantStdout:     "/wt/path\n",
			wantStderrHas:  []string{"Created worktree for branch 'foo'"},
			wantShellCalls: 0,
		},
		{
			name:           "shim+auto_cd_on_add: path on stdout",
			inShim:         true,
			autoCdOnAdd:    true,
			wantStdout:     "/wt/path\n",
			wantStderrHas:  []string{"Created worktree for branch 'foo'"},
			wantShellCalls: 0,
		},
		{
			name:           "shim+neither: stdout strictly empty",
			inShim:         true,
			wantStdout:     "",
			wantStderrHas:  []string{"Created worktree for branch 'foo'"},
			wantShellCalls: 0,
		},
		{
			name:           "shim+stay+expires: two stderr lines, one stdout line",
			inShim:         true,
			stay:           true,
			expiresAt:      &expAt,
			wantStdout:     "/wt/path\n",
			wantStderrHas:  []string{"Created worktree", "Worktree expires at"},
			wantShellCalls: 0,
		},
		{
			name:           "nonshim+stay: launchShell called, msg on stdout",
			stay:           true,
			wantStdout:     "Created worktree for branch 'foo' at /wt/path\n",
			wantShellCalls: 1,
		},
		{
			name:           "nonshim+no stay: no shell, msg on stdout",
			wantStdout:     "Created worktree for branch 'foo' at /wt/path\n",
			wantShellCalls: 0,
		},
		{
			name:           "auto_cd_on_add=true but no shim: no effect",
			autoCdOnAdd:    true,
			wantStdout:     "Created worktree for branch 'foo' at /wt/path\n",
			wantShellCalls: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer
			shellCalls := 0
			fakeLaunch := func(path string) error {
				shellCalls++
				if path != "/wt/path" {
					t.Errorf("launchShell path = %q; want %q", path, "/wt/path")
				}
				return nil
			}

			handleAddPostCreate(
				&stdout, &stderr,
				tt.inShim, tt.autoCdOnAdd,
				addResult{
					Branch:    "foo",
					Path:      "/wt/path",
					Stay:      tt.stay,
					ExpiresAt: tt.expiresAt,
				},
				fakeLaunch,
			)

			if got := stdout.String(); got != tt.wantStdout {
				t.Errorf("stdout = %q; want %q", got, tt.wantStdout)
			}
			for _, sub := range tt.wantStderrHas {
				if !strings.Contains(stderr.String(), sub) {
					t.Errorf("stderr = %q; want to contain %q", stderr.String(), sub)
				}
			}
			if shellCalls != tt.wantShellCalls {
				t.Errorf("launchShell calls = %d; want %d", shellCalls, tt.wantShellCalls)
			}
		})
	}
}

// The path is the whole point of the message for a non-interactive caller.
//
// It used to name only the branch, and reached stdout in exactly one case —
// shell integration with a cd wanted. Everyone else was told a worktree exists
// and left to work out where; an agent then looked in a directory it had
// guessed, found no package.json, and filed the empty directory as the bug
// (2026-08-19).
func TestHandleAddPostCreate_NamesThePath(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	handleAddPostCreate(&stdout, &stderr, false, false,
		addResult{Branch: "br", Path: "/scratch/launch/pi-house-telemetry"},
		func(string) error { return nil })

	if !strings.Contains(stdout.String(), "/scratch/launch/pi-house-telemetry") {
		t.Errorf("stdout = %q; want the created path in it", stdout.String())
	}
}

// …and the shell contract is untouched. The wrapper reads stdout and cds to it,
// so a path printed there when no cd was wanted, or a message printed there at
// all, would cd the operator somewhere or break on the message text. Under the
// shim the human-readable line belongs on stderr — which is exactly why the
// path was added to msgDst rather than to stdout.
func TestHandleAddPostCreate_ShimStdoutStaysBare(t *testing.T) {
	t.Parallel()

	// cd wanted: stdout is the path ALONE, nothing else.
	var out, errb bytes.Buffer
	handleAddPostCreate(&out, &errb, true, true,
		addResult{Branch: "br", Path: "/wt/p"}, func(string) error { return nil })
	if out.String() != "/wt/p\n" {
		t.Errorf("shim+cd stdout = %q; want the bare path", out.String())
	}
	if !strings.Contains(errb.String(), "/wt/p") {
		t.Errorf("shim stderr = %q; want the message to name the path too", errb.String())
	}

	// no cd wanted: stdout stays EMPTY so the wrapper's -n guard refuses to cd.
	var out2, errb2 bytes.Buffer
	handleAddPostCreate(&out2, &errb2, true, false,
		addResult{Branch: "br", Path: "/wt/p"}, func(string) error { return nil })
	if out2.String() != "" {
		t.Errorf("shim+no-cd stdout = %q; want empty", out2.String())
	}
}
