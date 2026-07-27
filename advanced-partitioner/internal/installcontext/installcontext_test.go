package installcontext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModeValidate(t *testing.T) {
	for _, m := range AllModes {
		if err := m.Validate(); err != nil {
			t.Fatalf("Validate(%q): %v", m, err)
		}
	}
	if err := Mode("memory").Validate(); err == nil {
		t.Fatal("expected error for invalid mode")
	} else if !strings.Contains(err.Error(), "memory") {
		t.Fatalf("error should quote bad value: %v", err)
	}
	// The pre-v1.4 enum values must now fail Validate.
	for _, m := range []Mode{"mem", "force-init", "init", "existing"} {
		if err := m.Validate(); err == nil {
			t.Fatalf("retired mode %q should fail Validate now", m)
		}
	}
	if err := Mode("").Validate(); err == nil {
		t.Fatal("empty mode should fail Validate")
	}
}

func TestModePredicates(t *testing.T) {
	cases := []struct {
		m                                                   Mode
		creates, extracts, local, memboot, sanitize, legacy bool
		bootloader                                          bool
	}{
		{ModeAuto, true, true, false, false, false, false, true},
		{ModeSync, true, true, false, false, false, false, true},
		{ModeFull, true, true, false, false, false, false, true},
		{ModeLocal, false, false, true, false, false, false, true},
		{ModeMemboot, false, true, false, true, false, false, false},
		{ModeSanitize, false, false, false, false, true, false, false},
		{ModeLegacy, false, false, false, false, false, true, false},
	}
	for _, c := range cases {
		if c.m.CreatesLayout() != c.creates || c.m.ExtractsImage() != c.extracts ||
			c.m.IsLocal() != c.local || c.m.IsMemboot() != c.memboot ||
			c.m.IsSanitize() != c.sanitize || c.m.IsLegacy() != c.legacy ||
			c.m.InstallsBootloader() != c.bootloader {
			t.Errorf("predicates wrong for %s", c.m)
		}
	}
}

func TestLoadDefaults(t *testing.T) {
	dir := t.TempDir()
	ctx, err := Load(LoadOptions{JSONPath: filepath.Join(dir, "does-not-exist.json")})
	if err != nil {
		t.Fatal(err)
	}
	if ctx.InstallMode != ModeAuto || ctx.Source != SourceDefault {
		t.Fatalf("defaults wrong: %+v", ctx)
	}
}

func TestLoadFileWins(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "ic.json")
	if err := os.WriteFile(jsonPath, []byte(`{"install_mode":"full"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, err := Load(LoadOptions{JSONPath: jsonPath})
	if err != nil {
		t.Fatal(err)
	}
	if ctx.InstallMode != ModeFull || ctx.Source != SourceFile {
		t.Fatalf("file value lost: %+v", ctx)
	}
}

func TestLoadExplicitBeatsFile(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "ic.json")
	if err := os.WriteFile(jsonPath, []byte(`{"install_mode":"full"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, err := Load(LoadOptions{ExplicitMode: ModeMemboot, JSONPath: jsonPath})
	if err != nil {
		t.Fatal(err)
	}
	if ctx.InstallMode != ModeMemboot || ctx.Source != SourceExplicit {
		t.Fatalf("explicit should win: %+v", ctx)
	}
}

func TestLoadInvalidFileValueFails(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "ic.json")
	if err := os.WriteFile(jsonPath, []byte(`{"install_mode":"banana"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(LoadOptions{JSONPath: jsonPath})
	if err == nil || !strings.Contains(err.Error(), "banana") {
		t.Fatalf("expected error mentioning bad value; got %v", err)
	}
}

func TestWriteJSONRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "out", "install-context.json")
	in := Context{InstallMode: ModeSanitize, Source: SourceFile}
	if err := WriteJSON(p, in); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	got, err := LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if got.InstallMode != in.InstallMode {
		t.Fatalf("round-trip lost values: %+v", got)
	}
	body, err := os.ReadFile(p) //nolint:gosec // test artefact
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"install_mode": "sanitize"`) {
		t.Fatalf("expected indented JSON; got:\n%s", body)
	}
}

func TestLoadFileMissingReturnsDefaults(t *testing.T) {
	ctx, err := LoadFile(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if ctx.InstallMode != ModeAuto {
		t.Fatalf("expected defaults; got %+v", ctx)
	}
}

func TestLoadFileMalformedFails(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(p); err == nil {
		t.Fatal("expected parse error")
	}
}
