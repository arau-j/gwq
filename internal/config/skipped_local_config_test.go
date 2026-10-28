package config

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// The TOML below is the real shape, copied from pyERP's `.gwq.toml`, not a
// tidied invention: `setup_commands` lives inside a `[[repository_settings]]`
// block, and the one command shells out to a script. A parser written against
// an imagined layout is how this class of defect survives.
const realLocalConfig = `
[[repository_settings]]
repository = "~/repos/pyERP{,.git,__worktrees,__worktrees/*}"
copy_files = ["config/env/.env.dev"]
setup_commands = [
  "bash scripts/gwq-worktree-setup.sh",
]
`

func TestDeclaredSetupCommandsReadsRepositorySettings(t *testing.T) {
	got := declaredSetupCommands([]byte(realLocalConfig))
	if len(got) != 1 || got[0] != "bash scripts/gwq-worktree-setup.sh" {
		t.Fatalf("declaredSetupCommands = %q, want the one command the block declares", got)
	}
}

func TestDeclaredSetupCommandsIsTotal(t *testing.T) {
	// It runs on a path that has already decided to skip the file, so anything
	// it cannot parse must degrade to "no commands" rather than blow up.
	for name, in := range map[string]string{
		"empty":              ``,
		"not toml at all":    "\x00\x01 not { toml",
		"no setup declared":  "[[repository_settings]]\nrepository = \"/x\"\n",
		"setup wrong type":   "[[repository_settings]]\nsetup_commands = 5\n",
		"settings not array": "repository_settings = 7\n",
	} {
		t.Run(name, func(t *testing.T) {
			if got := declaredSetupCommands([]byte(in)); len(got) != 0 {
				t.Errorf("declaredSetupCommands(%q) = %q, want none", in, got)
			}
		})
	}
}

// The whole point of the change: the warning must name the CONSEQUENCE, not
// just the file. An agent that reads only "skipping untrusted local config"
// goes looking for a broken toolchain when the toolchain was never installed.
func TestReportNamesWhatDidNotRun(t *testing.T) {
	var b bytes.Buffer
	reportSkippedLocalConfig(&b, "/repo/.gwq.toml", []byte(realLocalConfig))
	out := b.String()

	for _, want := range []string{
		"/repo/.gwq.toml",                    // which file
		"did NOT run",                        // what was lost
		"bash scripts/gwq-worktree-setup.sh", // what to run instead
	} {
		if !strings.Contains(out, want) {
			t.Errorf("warning does not mention %q:\n%s", want, out)
		}
	}
}

// A config that declares no setup commands has lost nothing this function can
// name, so it must not warn about setup that never existed. A warning that
// fires when nothing is wrong is how the real one stops being read.
func TestReportStaysQuietWhenNothingWasLost(t *testing.T) {
	var b bytes.Buffer
	reportSkippedLocalConfig(&b, "/repo/.gwq.toml", []byte("[[repository_settings]]\nrepository = \"/x\"\n"))
	out := b.String()

	if !strings.Contains(out, "skipping untrusted local config") {
		t.Errorf("dropped the original warning entirely:\n%s", out)
	}
	if strings.Contains(out, "did NOT run") {
		t.Errorf("warned about setup_commands the config never declared:\n%s", out)
	}
}

// `.gwq.toml` is untrusted input on a path that exists BECAUSE it is untrusted.
// Echoing it raw would let a hostile config drive the terminal that is warning
// about it — clear the screen, move the cursor, forge a later prompt.
func TestReportSanitizesUntrustedContent(t *testing.T) {
	hostile := "[[repository_settings]]\nsetup_commands = [\"\\u001b[2Jwiped\"]\n"
	var b bytes.Buffer
	reportSkippedLocalConfig(&b, "/repo/\x1b[2J.gwq.toml", []byte(hostile))
	out := b.String()

	if strings.ContainsRune(out, '\x1b') {
		t.Errorf("raw escape byte reached the terminal:\n%q", out)
	}
	// Still says what it was for — sanitising must not silently drop the text.
	if !strings.Contains(out, "wiped") {
		t.Errorf("sanitising removed the command text entirely:\n%q", out)
	}
}

// The echo is bounded. A diagnostic that reprints an arbitrarily long file
// costs more attention than the failure it explains.
func TestReportCapsTheCommandList(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("[[repository_settings]]\nsetup_commands = [\n")
	for i := 0; i < maxReportedSetupCommands+5; i++ {
		sb.WriteString("  \"cmd-" + string(rune('a'+i)) + "\",\n")
	}
	sb.WriteString("]\n")

	var b bytes.Buffer
	reportSkippedLocalConfig(&b, "/repo/.gwq.toml", []byte(sb.String()))
	out := b.String()

	if n := strings.Count(out, "gwq:     cmd-"); n != maxReportedSetupCommands {
		t.Errorf("printed %d commands, want the cap of %d", n, maxReportedSetupCommands)
	}
	if !strings.Contains(out, "and 5 more") {
		t.Errorf("capped the list without saying how many it withheld:\n%s", out)
	}
}

// THE WIRING. Everything above tests the reporter directly; none of it proves
// mergeLocalConfig calls it. The defect was that the skip said too little, and a
// reporter nobody invokes says nothing at all — a negative control reverting
// only the call site left every test above passing, which is exactly the shape
// this test exists to close.
//
// It drives the real mergeLocalConfig on the real untrusted path (empty trust
// store, interactive=false) and reads what actually reached stderr.
func TestMergeLocalConfigReportsTheSkippedSetup(t *testing.T) {
	viper.Reset()
	t.Cleanup(func() { viper.Reset() })

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gwq.toml"), []byte(realLocalConfig), 0o644); err != nil {
		t.Fatalf("write local config: %v", err)
	}
	changeDir(t, dir)

	restore := captureStderr(t)
	// An empty trust store trusts nothing, and interactive=false is the
	// non-TTY session every launched agent runs in.
	err := mergeLocalConfig(&TrustStore{}, trustingPrompter(), false)
	out := restore()

	if err != nil {
		t.Fatalf("mergeLocalConfig() error = %v", err)
	}
	if !strings.Contains(out, "did NOT run") {
		t.Errorf("the skip did not say the setup_commands were lost:\n%s", out)
	}
	if !strings.Contains(out, "bash scripts/gwq-worktree-setup.sh") {
		t.Errorf("the skip did not name the command to run instead:\n%s", out)
	}
}

// captureStderr redirects os.Stderr for the duration of one call. mergeLocalConfig
// writes there directly, so this is the only way to observe it without changing
// the signature every other caller depends on.
func captureStderr(t *testing.T) func() string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	return func() string {
		os.Stderr = orig
		_ = w.Close()
		s := <-done
		_ = r.Close()
		return s
	}
}
