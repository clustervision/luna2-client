package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/result"
)

// Per the cross-tool convention, cmd-level tests cover the orchestrator
// + storage-result loading + the boot_method=none path. The disk-touching
// grub2-install / dracut / efibootmgr flows are exercised by the on-node
// integration runs from /root/NEWTREE/regression.md; per-module tests in
// internal/bootloader/* already cover them with FakeRunner.

func runEnv(t *testing.T, args ...string) (stderr string, code int) {
	t.Helper()
	tmp := t.TempDir()
	cmdline := filepath.Join(tmp, "cmdline")
	if err := os.WriteFile(cmdline, []byte("root=luna rw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(tmp, "logs")
	args = append([]string{
		"--log-dir", logDir,
		"--cmdline-path", cmdline,
		"--skip-command-check",
	}, args...)
	var outBuf, errBuf bytes.Buffer
	rc := run(append([]string{"lpart-bootloader-finalise"}, args...), &outBuf, &errBuf)
	return errBuf.String(), rc
}

// stageTmpfsResult writes a storage-result.json that mirrors what
// lpart-storage-prepare produces for a memory root. Returns the path.
func stageTmpfsResult(t *testing.T, sysroot string) string {
	t.Helper()
	res := result.StorageResult{
		Version:           2,
		Name:              "os",
		Role:              "os",
		FirmwareMode:      "memory",
		StorageType:       "memory",
		BootMethod:        "none",
		BootFirmware:      "auto",
		RegisterNVRAM:     "auto",
		ClearUEFINVRAM:    true,
		RAIDMode:          "none",
		MountedTargetRoot: sysroot,
		Filesystems: []result.Filesystem{
			{Mount: "/", Device: "tmpfs", FS: "tmpfs", Options: "defaults", ClearUEFINVRAM: true},
		},
	}
	body, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "storage-result.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMissingResultFile(t *testing.T) {
	stderr, rc := runEnv(t, "--result", "/no/such/file.json")
	if rc == 0 {
		t.Fatalf("expected non-zero rc; stderr=%s", stderr)
	}
	if !strings.Contains(stderr, "storage-result.json not found") {
		t.Fatalf("wording wrong: %s", stderr)
	}
}

func TestRefusesWithoutLunaMarker(t *testing.T) {
	tmp := t.TempDir()
	badCmdline := filepath.Join(tmp, "cmdline-bad")
	if err := os.WriteFile(badCmdline, []byte("root=/dev/sda1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sysroot := t.TempDir()
	resPath := stageTmpfsResult(t, sysroot)
	var outBuf, errBuf bytes.Buffer
	rc := run([]string{
		"lpart-bootloader-finalise",
		"--log-dir", filepath.Join(tmp, "logs"),
		"--cmdline-path", badCmdline,
		"--skip-command-check",
		"--result", resPath,
	}, &outBuf, &errBuf)
	if rc == 0 {
		t.Fatalf("expected refusal; stderr=%s", errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "root=luna") {
		t.Fatalf("wording wrong: %s", errBuf.String())
	}
}

// boot_method=none + clear_uefi_nvram=false: fstab only, no bind mounts.
func TestTmpfsRootFstabOnly(t *testing.T) {
	tmp := t.TempDir()
	sysroot := filepath.Join(tmp, "sysroot")
	if err := os.MkdirAll(sysroot, 0o750); err != nil {
		t.Fatal(err)
	}
	// Write a storage-result with clear_uefi_nvram=false so we exercise
	// the simplest no-NVRAM path.
	res := result.StorageResult{
		Version:           2,
		Name:              "os",
		Role:              "os",
		FirmwareMode:      "memory",
		StorageType:       "memory",
		BootMethod:        "none",
		BootFirmware:      "auto",
		RegisterNVRAM:     "auto",
		ClearUEFINVRAM:    false,
		RAIDMode:          "none",
		MountedTargetRoot: sysroot,
		Filesystems: []result.Filesystem{
			{Mount: "/", Device: "tmpfs", FS: "tmpfs", Options: "defaults"},
		},
	}
	body, _ := json.Marshal(res)
	resPath := filepath.Join(tmp, "storage-result.json")
	if err := os.WriteFile(resPath, body, 0o600); err != nil {
		t.Fatal(err)
	}

	bootCtxOut := filepath.Join(tmp, "boot-context.json")
	stderr, rc := runEnv(t,
		"--result", resPath,
		"--boot-context-out", bootCtxOut,
	)
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, stderr)
	}
	// fstab should be written.
	fstab, err := os.ReadFile(filepath.Join(sysroot, "etc", "fstab")) //nolint:gosec // test artefact
	if err != nil {
		t.Fatalf("fstab missing: %v", err)
	}
	if !strings.Contains(string(fstab), "tmpfs / tmpfs defaults 0 0") {
		t.Fatalf("fstab content wrong:\n%s", fstab)
	}
	// boot-context.json should be written with family=none.
	bctx, err := os.ReadFile(bootCtxOut) //nolint:gosec // test artefact
	if err != nil {
		t.Fatalf("boot-context missing: %v", err)
	}
	if !strings.Contains(string(bctx), `"family": "none"`) {
		t.Fatalf("boot-context family wrong:\n%s", bctx)
	}
}

func TestStorageResultMissingSysrootDirectory(t *testing.T) {
	tmp := t.TempDir()
	res := result.StorageResult{
		Version:           2,
		BootMethod:        "none",
		MountedTargetRoot: "/no/such/sysroot",
		Filesystems:       []result.Filesystem{{Mount: "/", FS: "tmpfs"}},
	}
	body, _ := json.Marshal(res)
	resPath := filepath.Join(tmp, "storage-result.json")
	if err := os.WriteFile(resPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	stderr, rc := runEnv(t, "--result", resPath)
	if rc == 0 {
		t.Fatalf("expected non-zero rc; stderr=%s", stderr)
	}
	if !strings.Contains(stderr, "missing sysroot directory") {
		t.Fatalf("wording wrong: %s", stderr)
	}
}

func TestMultiSetResultExtractsOS(t *testing.T) {
	tmp := t.TempDir()
	sysroot := filepath.Join(tmp, "sysroot")
	if err := os.MkdirAll(sysroot, 0o750); err != nil {
		t.Fatal(err)
	}
	ms := result.MultiSet{
		Version: 2,
		Sets: []result.StorageResult{
			{
				Name: "os", Role: "os",
				FirmwareMode: "memory", StorageType: "memory",
				BootMethod: "none", MountedTargetRoot: sysroot,
				Filesystems: []result.Filesystem{
					{Mount: "/", FS: "tmpfs", Device: "tmpfs", Options: "defaults"},
				},
			},
			{
				Name: "data", Role: "data",
				FirmwareMode: "uefi", StorageType: "lvm",
				MountedTargetRoot: sysroot,
				Filesystems: []result.Filesystem{
					{Mount: "/data", FS: "xfs", Device: "/dev/mapper/data-data", UUID: "data-uuid"},
				},
			},
		},
	}
	body, _ := json.Marshal(ms)
	resPath := filepath.Join(tmp, "storage-result.json")
	if err := os.WriteFile(resPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	stderr, rc := runEnv(t, "--result", resPath)
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, stderr)
	}
	// fstab should mention both / (tmpfs) and /data (data set merged in).
	fstab, _ := os.ReadFile(filepath.Join(sysroot, "etc", "fstab")) //nolint:gosec // test artefact
	if !strings.Contains(string(fstab), "tmpfs / tmpfs defaults 0 0") {
		t.Fatalf("fstab missing tmpfs / entry:\n%s", fstab)
	}
	if !strings.Contains(string(fstab), "UUID=data-uuid /data xfs defaults 0 0") {
		t.Fatalf("fstab missing /data entry (merged data set):\n%s", fstab)
	}
}
