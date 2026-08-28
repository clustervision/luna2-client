package lpartinputs

import (
	"encoding/json"
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
	got := readInstallContext(filepath.Join(dir, "install-context.json"))
	if got.InstallMode != "memboot" {
		t.Fatalf("round-trip mode = %s", got.InstallMode)
	}
	// Provision fields must round-trip so the download honours them.
	if got.ProvisionMethod != "http" || got.ProvisionFallback != "torrent" {
		t.Fatalf("provision round-trip = %+v", got)
	}
	if m := readInstallContext(filepath.Join(dir, "missing.json")); m.InstallMode != "auto" {
		t.Fatalf("default = %s, want auto", m.InstallMode)
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
