package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRefusesOutsideInstallerEnv locks in the installer-env guard on the
// orchestrator: with a non-Luna cmdline and no --allow-non-installer, run()
// must refuse before doing any phase work (it drives the destructive lpart-*
// tools). run() uses the global flag set, so reset it around the call.
func TestRefusesOutsideInstallerEnv(t *testing.T) {
	tmp := t.TempDir()
	cmdline := filepath.Join(tmp, "cmdline")
	if err := os.WriteFile(cmdline, []byte("root=/dev/sda1 rw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldArgs, oldFlags := os.Args, flag.CommandLine
	defer func() { os.Args, flag.CommandLine = oldArgs, oldFlags }()
	flag.CommandLine = flag.NewFlagSet("lpart-node-installer", flag.ContinueOnError)
	os.Args = []string{"lpart-node-installer", "--phase", "pre", "--cmdline-path", cmdline}

	err := run()
	if err == nil {
		t.Fatal("expected refusal outside the installer environment")
	}
	if !strings.Contains(err.Error(), "root=luna") {
		t.Errorf("error should mention the root=luna marker, got: %v", err)
	}
}

// TestLegacyRefusal locks in "legacy means legacy: no TUI": when the
// daemon-written install-context.json says install_mode=legacy, the orchestrator
// must refuse up-front (non-empty message -> exit exitFatalConfig, no TUI). Any
// other mode, or a missing file, proceeds (empty message).
func TestLegacyRefusal(t *testing.T) {
	tests := []struct {
		name       string
		mode       string // "" => write no file at all
		wantRefuse bool
	}{
		{"legacy refuses", "legacy", true},
		{"auto proceeds", "auto", false},
		{"full proceeds", "full", false},
		{"missing file proceeds", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.mode != "" {
				p := filepath.Join(dir, "install-context.json")
				body := []byte(`{"install_mode":"` + tc.mode + `"}`)
				if err := os.WriteFile(p, body, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			msg := legacyRefusal(dir)
			if (msg != "") != tc.wantRefuse {
				t.Fatalf("legacyRefusal(mode=%q) = %q; wantRefuse=%v", tc.mode, msg, tc.wantRefuse)
			}
			if tc.wantRefuse && !strings.Contains(msg, "no TUI") {
				t.Errorf("refusal message should mention 'no TUI', got: %q", msg)
			}
		})
	}
}
