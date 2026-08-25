package config

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/viper"
)

// What to say when a local `.gwq.toml` is skipped for want of trust.
//
// The trust gate itself is right: a repo-local config can name commands, and
// gwq must not execute them because someone cloned a repository. But in a
// NON-INTERACTIVE session there is no prompt to answer, so the file is skipped
// and the run continues — and the one line it printed named the file and
// stopped there:
//
//	gwq: skipping untrusted local config /repo/.gwq.toml (non-interactive session)
//
// That says what gwq declined to read. It does not say what the caller LOST,
// which is the only part they can act on: the config's `setup_commands` did not
// run, so the worktree has no submodules, no `node_modules`, no `.venv`. The
// failure surfaces much later and somewhere else, wearing a completely
// unrelated face —
//
//	sh: line 1: tsc: command not found
//	sh: line 1: vitest: command not found
//	MODULE_NOT_FOUND
//
// — none of which mentions gwq, a trust store, or a config file. Four papercuts
// in the two days to 2026-08-19 are that chain, across pi-house and
// ASFAM-PMware, and the agents that hit it went looking for a broken toolchain.
//
// This is the same failure as the missing-`repository_settings` case in
// post_setup.go and the unprinted path in add.go: gwq skipped the setup and the
// caller could not tell. Three causes, one symptom, so all three now say so.
//
// The remedy named is the one a non-interactive caller can actually take: run
// the commands. Granting trust needs a TTY by construction, so telling an agent
// to grant trust would be prescribing something it cannot do — the very shape of
// failure this function exists to remove.

// maxReportedSetupCommands bounds the echo. A config with more than this is
// already unusual, and the point is to name the work that did not happen, not to
// reproduce the file.
const maxReportedSetupCommands = 10

// declaredSetupCommands returns the setup commands a local config declares,
// across every `[[repository_settings]]` block plus any top-level key.
//
// Parsing is best-effort and read-only: this runs on a path that has ALREADY
// decided to skip the file, so a parse failure must degrade to the shorter
// message rather than turn a warning into an error. Nothing here is executed —
// the trust gate still holds, and reading a file to describe it is what the
// interactive prompt already does.
func declaredSetupCommands(data []byte) []string {
	v := viper.New()
	v.SetConfigType(configType)
	if err := v.ReadConfig(bytes.NewReader(data)); err != nil {
		return nil
	}

	var out []string
	out = append(out, v.GetStringSlice("setup_commands")...)
	if settings, ok := v.Get("repository_settings").([]any); ok {
		for _, entry := range settings {
			m, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			raw, ok := m["setup_commands"].([]any)
			if !ok {
				continue
			}
			for _, c := range raw {
				if s, ok := c.(string); ok && s != "" {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// reportSkippedLocalConfig writes the warning for a local config that was
// skipped because the session cannot ask whether to trust it.
//
// Every piece of file-derived text goes through sanitizeForTerminal for the
// reason the interactive prompt already documents: a `.gwq.toml` is untrusted
// input, and an ANSI or OSC sequence inside one must not reach the terminal that
// is warning about it.
func reportSkippedLocalConfig(w io.Writer, absPath string, data []byte) {
	var b strings.Builder
	fmt.Fprintf(&b, "gwq: skipping untrusted local config %s (non-interactive session)\n",
		sanitizeForTerminal(absPath))

	// Nothing beyond the file name was lost that this function can name, so it
	// says only that — a warning that fires when nothing is wrong is how the
	// real one stops being read.
	if cmds := declaredSetupCommands(data); len(cmds) > 0 {
		shown := cmds
		if len(shown) > maxReportedSetupCommands {
			shown = shown[:maxReportedSetupCommands]
		}
		b.WriteString("gwq:   Its setup_commands did NOT run, so this worktree has no project setup —\n")
		b.WriteString("gwq:   expect missing dependencies (`command not found`, MODULE_NOT_FOUND).\n")
		b.WriteString("gwq:   Run them yourself in the new worktree:\n")
		for _, c := range shown {
			fmt.Fprintf(&b, "gwq:     %s\n", sanitizeForTerminal(c))
		}
		if len(cmds) > len(shown) {
			fmt.Fprintf(&b, "gwq:     … and %d more\n", len(cmds)-len(shown))
		}
	}

	// One write, so the whole warning cannot be interleaved with another
	// writer's output on the same stderr.
	_, _ = io.WriteString(w, b.String())
}
