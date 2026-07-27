package lpartinputs

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteJSONField(t *testing.T) {
	dir := t.TempDir()

	b64 := base64.StdEncoding.EncodeToString([]byte(`{"a":1}`))
	p := filepath.Join(dir, "s.json")
	if !writeJSONField(p, b64) {
		t.Fatal("base64 JSON should populate")
	}
	got, err := os.ReadFile(p) //nolint:gosec // test temp path
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":1}` {
		t.Fatalf("decoded = %q, want {\"a\":1}", got)
	}

	if !writeJSONField(filepath.Join(dir, "s2.json"), `{"b":2}`) {
		t.Fatal("plain JSON should populate")
	}
	if writeJSONField(filepath.Join(dir, "e.json"), "") {
		t.Fatal("empty value should not populate")
	}
	if writeJSONField(filepath.Join(dir, "g.json"), "notjson") {
		t.Fatal("non-JSON garbage should not populate")
	}
}

func TestExtractNodeFields(t *testing.T) {
	body := []byte(`{"config":{"node":{"node001":{"install_mode":"full","disklayout":"eyJhIjoxfQ=="}}}}`)
	f, err := extractNodeFields(body, "node001")
	if err != nil {
		t.Fatal(err)
	}
	if f.InstallMode != "full" {
		t.Fatalf("fields = %+v", f)
	}
	if f.Disklayout == "" {
		t.Fatal("disklayout missing")
	}
}

func TestWriteInputsDefaultsModeAuto(t *testing.T) {
	dir := t.TempDir()
	// No install_mode in the daemon response -> defaults to auto.
	in := writeInputs(dir, nodeFields{Disklayout: ""})
	if in.InstallMode != "auto" {
		t.Fatalf("absent install_mode should default to auto, got %q", in.InstallMode)
	}
	// A populated install_mode is carried through verbatim.
	in = writeInputs(dir, nodeFields{InstallMode: "memboot"})
	if in.InstallMode != "memboot" {
		t.Fatalf("install_mode should pass through, got %q", in.InstallMode)
	}
}

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
	writeInstallContext(dir, installContext{InstallMode: "full"})

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
}
