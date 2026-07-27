package sysenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequireInstallerEnvHappy(t *testing.T) {
	dir := t.TempDir()
	cmdline := filepath.Join(dir, "cmdline")
	if err := os.WriteFile(cmdline, []byte("BOOT_IMAGE=vmlinuz root=luna rw quiet\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RequireInstallerEnv(Options{CmdlinePath: cmdline}); err != nil {
		t.Fatalf("want pass; got %v", err)
	}
}

func TestRequireInstallerEnvRefusesWithoutMarker(t *testing.T) {
	dir := t.TempDir()
	cmdline := filepath.Join(dir, "cmdline")
	if err := os.WriteFile(cmdline, []byte("BOOT_IMAGE=vmlinuz root=/dev/sda1 rw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := RequireInstallerEnv(Options{CmdlinePath: cmdline})
	if err == nil {
		t.Fatal("expected refusal")
	}
	if !strings.Contains(err.Error(), "root=luna") {
		t.Fatalf("wording wrong: %v", err)
	}
}

func TestRequireInstallerEnvAllowSkip(t *testing.T) {
	// Nonexistent path; AllowNonInstaller short-circuits the probe.
	if err := RequireInstallerEnv(Options{
		CmdlinePath:       "/no/such/file",
		AllowNonInstaller: true,
	}); err != nil {
		t.Fatalf("expected pass with AllowNonInstaller; got %v", err)
	}
}

func TestRequireInstallerEnvMissingCmdline(t *testing.T) {
	err := RequireInstallerEnv(Options{CmdlinePath: "/no/such/file"})
	if err == nil {
		t.Fatal("expected error when cmdline file missing")
	}
}

func TestRequireExpectedHostMatchesShortName(t *testing.T) {
	if err := RequireExpectedHost("node001", Options{
		Hostname: func() (string, error) { return "node001.cluster", nil },
	}); err != nil {
		t.Fatalf("FQDN should match short name: %v", err)
	}
}

func TestRequireExpectedHostMismatch(t *testing.T) {
	err := RequireExpectedHost("node001", Options{
		Hostname: func() (string, error) { return "node002", nil },
	})
	if err == nil || !strings.Contains(err.Error(), "expected node001") {
		t.Fatalf("expected mismatch error; got %v", err)
	}
}

func TestRequireExpectedHostNoOpWhenEmpty(t *testing.T) {
	if err := RequireExpectedHost("", Options{
		Hostname: func() (string, error) { return "", nil },
	}); err != nil {
		t.Fatalf("empty expected should be a no-op; got %v", err)
	}
}

func TestRequireCommandsTrue(t *testing.T) {
	// `true` exists on every Linux host the tests run on.
	if err := RequireCommands("true"); err != nil {
		t.Fatalf("true should be on PATH: %v", err)
	}
}

func TestRequireCommandsMissing(t *testing.T) {
	err := RequireCommands("definitely-not-a-real-command-12345")
	if err == nil {
		t.Fatal("expected missing-command error")
	}
	if !strings.Contains(err.Error(), "missing required commands") {
		t.Fatalf("wording wrong: %v", err)
	}
}

func TestRequireCommandsWithFallback(t *testing.T) {
	// `true` is the primary; the fallback list shouldn't be touched.
	chosen, err := RequireCommandsWithFallback("true", "definitely-not-real")
	if err != nil {
		t.Fatalf("true should be picked: %v", err)
	}
	if chosen != "true" {
		t.Fatalf("chosen = %q, want true", chosen)
	}
	// Fallback path: primary missing but the alternative is present.
	chosen, err = RequireCommandsWithFallback("definitely-not-real", "true")
	if err != nil {
		t.Fatalf("fallback to 'true' should succeed: %v", err)
	}
	if chosen != "true" {
		t.Fatalf("chosen = %q, want true (fallback)", chosen)
	}
	// All missing -> error mentions every alternative.
	_, err = RequireCommandsWithFallback("nope-1", "nope-2", "nope-3")
	if err == nil || !strings.Contains(err.Error(), "nope-1") {
		t.Fatalf("expected fallback error mentioning every name; got %v", err)
	}
}

func TestKernelParam(t *testing.T) {
	dir := t.TempDir()
	cmdline := filepath.Join(dir, "cmdline")
	if err := os.WriteFile(cmdline,
		[]byte("BOOT_IMAGE=x root=luna luna.url=https://ctrl:7050 luna.node=node001 quiet\n"),
		0o600); err != nil {
		t.Fatal(err)
	}
	opts := Options{CmdlinePath: cmdline}

	if v, ok := KernelParam(opts, "luna.url"); !ok || v != "https://ctrl:7050" {
		t.Errorf("luna.url: got %q ok=%v", v, ok)
	}
	if v, ok := KernelParam(opts, "luna.node"); !ok || v != "node001" {
		t.Errorf("luna.node: got %q ok=%v", v, ok)
	}
	if _, ok := KernelParam(opts, "luna.missing"); ok {
		t.Errorf("absent key must report ok=false")
	}
	if _, ok := KernelParam(Options{CmdlinePath: filepath.Join(dir, "nope")}, "luna.url"); ok {
		t.Errorf("unreadable cmdline must report ok=false")
	}
}
