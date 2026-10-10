package lpartinputs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallContextRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writeInstallContext(dir, installContext{
		InstallMode:       "memboot",
		ProvisionMethod:   "http",
		ProvisionFallback: "torrent",
	})
	got, err := readInstallContext(filepath.Join(dir, "install-context.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got.InstallMode != "memboot" {
		t.Fatalf("round-trip mode = %s", got.InstallMode)
	}
	// Provision fields must round-trip so the download honours them.
	if got.ProvisionMethod != "http" || got.ProvisionFallback != "torrent" {
		t.Fatalf("provision round-trip = %+v", got)
	}
	if m, err := readInstallContext(filepath.Join(dir, "missing.json")); err != nil || m.InstallMode != "auto" {
		t.Fatalf("default = %s (%v), want auto", m.InstallMode, err)
	}
}

// A malformed install-context.json must not silently become "auto" (an
// intended local/full/sanitize would then converge a healthy disk).
func TestMalformedInstallContextIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "install-context.json"), []byte(`{"install_mode": "local",`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve(dir)
	var ie *ErrInstallContext
	if !errors.As(err, &ie) {
		t.Fatalf("want ErrInstallContext, got %v", err)
	}
}

func TestResolveFromFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "storage.json"), []byte(`{"x":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// The install template writes install_mode AND the provision_* fields.
	writeInstallContext(dir, installContext{
		InstallMode:        "full",
		ProvisionMethod:    "torrent",
		ProvisionFallback:  "http",
		ProvisionInterface: "BOOTIF",
	})

	in, err := resolveFromFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !in.DisklayoutPopulated {
		t.Fatalf("populated flags = %+v", in)
	}
	if in.InstallMode != "full" || in.Source != "file" {
		t.Fatalf("resolved = %+v", in)
	}

	// The provision_* the template supplied must survive lpart's normalisation,
	// since the download phase reads them back out of install-context.json.
	var ic installContext
	data, err := os.ReadFile(filepath.Join(dir, "install-context.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &ic); err != nil {
		t.Fatal(err)
	}
	if ic.ProvisionMethod != "torrent" || ic.ProvisionFallback != "http" || ic.ProvisionInterface != "BOOTIF" {
		t.Fatalf("provision_* not preserved through Resolve: %+v", ic)
	}
}

// Resolve is the public entry point; with only the template files present it
// must resolve from them and report Source "file".
func TestResolvePublicEntry(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime") // not yet created; Resolve mkdirs it
	writeDir := dir
	if err := os.MkdirAll(writeDir, 0o750); err != nil {
		t.Fatal(err)
	}
	writeInstallContext(writeDir, installContext{InstallMode: "sync"})
	in, err := Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	if in.Source != "file" || in.InstallMode != "sync" {
		t.Fatalf("Resolve = %+v", in)
	}
}
