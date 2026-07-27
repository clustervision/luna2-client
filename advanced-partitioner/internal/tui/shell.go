package tui

import (
	"os"
	"os/exec"
)

// execBash returns the command used by F2 Shell handoff. Honours $SHELL
// when set, defaults to /bin/bash otherwise. The command runs attached
// to the existing TTY; Bubble Tea's tea.ExecProcess takes care of the
// suspend/resume dance.
func execBash() *exec.Cmd {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	return exec.Command(shell, "-l") //nolint:gosec // operator-provided shell
}
