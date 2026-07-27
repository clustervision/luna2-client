package osimage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectImageFilenameFromInstallScript(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "install.sh")
	body := `#!/bin/bash
DOWNLOAD_URL="http://10.141.255.254:7051/files/compute-image-1779269535.tar.bz2"
LUNA_TOKEN="abc123"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := DetectImageFilename(DetectOptions{LunaInstallScript: path})
	if err != nil {
		t.Fatalf("DetectImageFilename: %v", err)
	}
	if got != "compute-image-1779269535.tar.bz2" {
		t.Fatalf("got %q", got)
	}
}

func TestDetectImageFilenameOverride(t *testing.T) {
	got, err := DetectImageFilename(DetectOptions{Override: "my-image.tar.bz2"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "my-image.tar.bz2" {
		t.Fatalf("override ignored; got %q", got)
	}
}

func TestDetectImageFilenameMissingScript(t *testing.T) {
	_, err := DetectImageFilename(DetectOptions{LunaInstallScript: "/no/such/file"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDetectImageFilenameUnknownFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "install.sh")
	if err := os.WriteFile(path, []byte("no image url here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := DetectImageFilename(DetectOptions{LunaInstallScript: path})
	if err == nil {
		t.Fatal("expected error when filename not in file")
	}
}

func TestDetectLunaToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "install.sh")
	if err := os.WriteFile(path, []byte(`LUNA_TOKEN="t0p_s3cr3t"`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := DetectLunaToken(DetectOptions{LunaInstallScript: path})
	if err != nil {
		t.Fatal(err)
	}
	if got != "t0p_s3cr3t" {
		t.Fatalf("got %q", got)
	}
}

func TestDetectLunaTokenOverride(t *testing.T) {
	got, err := DetectLunaToken(DetectOptions{Override: "envtoken"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "envtoken" {
		t.Fatalf("override lost: %q", got)
	}
}

func TestDetectLunaTokenMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "install.sh")
	if err := os.WriteFile(path, []byte("no token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := DetectLunaToken(DetectOptions{LunaInstallScript: path})
	if err == nil {
		t.Fatal("expected error when token missing")
	}
}

func TestLoadFilterConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "filter.json")
	body := `{
		"preserve":       ["var/log/***", "etc/ssh/ssh_host_*"],
		"preserve_empty": ["home", "tmp"],
		"no_new_files":   ["proc", "sys"]
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFilterConfig(path)
	if err != nil {
		t.Fatalf("LoadFilterConfig: %v", err)
	}
	if len(cfg.Preserve) != 2 || cfg.Preserve[0] != "var/log/***" {
		t.Fatalf("Preserve wrong: %+v", cfg.Preserve)
	}
	if len(cfg.PreserveEmpty) != 2 {
		t.Fatalf("PreserveEmpty len = %d", len(cfg.PreserveEmpty))
	}
	if len(cfg.NoNewFiles) != 2 {
		t.Fatalf("NoNewFiles len = %d", len(cfg.NoNewFiles))
	}
}

func TestLoadFilterConfigEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "filter.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFilterConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Preserve)+len(cfg.PreserveEmpty)+len(cfg.NoNewFiles) != 0 {
		t.Fatalf("empty config should yield empty slices: %+v", cfg)
	}
}
