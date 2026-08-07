package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// fakeResolver stands in for git so the decision table stays about the decision
// rather than about repository fixtures — those are covered in internal/git.
type fakeResolver struct {
	defaultRef string
	fetchErr   error
	fetched    []string
}

func (f *fakeResolver) DefaultBaseRef(string) string { return f.defaultRef }

func (f *fakeResolver) FetchRef(ref string) error {
	f.fetched = append(f.fetched, ref)
	return f.fetchErr
}

func TestNewBranchBase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		explicit    string
		defaultRef  string
		fetchErr    error
		wantRef     string
		wantDefault bool
		wantStale   bool
		wantFetched []string
	}{
		{
			name:        "no --from: the remote default, freshly fetched",
			defaultRef:  "origin/main",
			wantRef:     "origin/main",
			wantDefault: true,
			wantFetched: []string{"origin/main"},
		},
		{
			name:        "explicit --from wins and is not announced",
			explicit:    "origin/release-2",
			defaultRef:  "origin/main",
			wantRef:     "origin/release-2",
			wantFetched: []string{"origin/release-2"},
		},
		{
			// The escape hatch: stacking a branch on the work in front of you
			// is the one case the old default served, and it must stay
			// reachable — with no fetch, since HEAD is local.
			name:     "--from HEAD keeps git's own behaviour",
			explicit: "HEAD",
		},
		{
			// A local-only repository has nothing to default to. Failing a
			// worktree creation over a missing remote would be worse than the
			// bug being fixed.
			name: "no remote default: fall through to git",
		},
		{
			name:        "unreachable remote is reported, not fatal",
			defaultRef:  "origin/main",
			fetchErr:    errors.New("could not resolve host"),
			wantRef:     "origin/main",
			wantDefault: true,
			wantStale:   true,
			wantFetched: []string{"origin/main"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeResolver{defaultRef: tt.defaultRef, fetchErr: tt.fetchErr}

			got := newBranchBase(f, tt.explicit)

			if got.Ref != tt.wantRef {
				t.Errorf("Ref = %q, want %q", got.Ref, tt.wantRef)
			}
			if got.Defaulted != tt.wantDefault {
				t.Errorf("Defaulted = %v, want %v", got.Defaulted, tt.wantDefault)
			}
			if got.Stale != tt.wantStale {
				t.Errorf("Stale = %v, want %v", got.Stale, tt.wantStale)
			}
			if len(f.fetched) != len(tt.wantFetched) {
				t.Errorf("fetched %v, want %v", f.fetched, tt.wantFetched)
			}
		})
	}
}

// The defaulted base is printed because a silently chosen start point is how
// the old behaviour stayed invisible for so long.
func TestHandleAddPostCreateAnnouncesADefaultedBase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		base baseChoice
		want string
	}{
		{"defaulted", baseChoice{Ref: "origin/main", Defaulted: true}, "Branched from origin/main"},
		{"stale", baseChoice{Ref: "origin/main", Defaulted: true, Stale: true}, "may be stale"},
		{"explicit is not announced", baseChoice{Ref: "origin/release-2"}, ""},
		{"no base is not announced", baseChoice{}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			handleAddPostCreate(&stdout, &stderr, false, false,
				addResult{Branch: "foo", Path: "/wt/path", Base: tt.base},
				func(string) error { return nil })

			out := stdout.String()
			if tt.want == "" {
				if strings.Contains(out, "Branched from") {
					t.Fatalf("unexpected base line in:\n%s", out)
				}
				return
			}
			if !strings.Contains(out, tt.want) {
				t.Fatalf("missing %q in:\n%s", tt.want, out)
			}
		})
	}
}
