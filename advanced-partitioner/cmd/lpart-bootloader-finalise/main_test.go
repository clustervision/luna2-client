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

// Belt behind lpart-storage-prepare's boot preflight: GRUB is UEFI-only, so a
// grub boot method on BIOS firmware is an upstream bug. It must refuse with
// exit 2 (park) before the bind mounts and the adapter. Without the belt this
// run reaches the bind mounts and fails with exit 1 (a retry loop), so rc 2 is
// what proves the belt fired first.
func TestGrubOnBIOSRefusedBeforeDracut(t *testing.T) {
	tmp := t.TempDir()
	sysroot := filepath.Join(tmp, "sysroot")
	if err := os.MkdirAll(filepath.Join(sysroot, "etc"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sysroot, "etc", "os-release"),
		[]byte("ID=rocky\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := result.StorageResult{
		Version: 2, Name: "os", Role: "os",
		FirmwareMode: "bios", StorageType: "lvm",
		BootMethod: "grub", BootFirmware: "uefi", RegisterNVRAM: "auto",
		RAIDMode: "none", MountedTargetRoot: sysroot,
		BootDevices: []string{"/dev/sda"},
		Filesystems: []result.Filesystem{
			{Mount: "/", Device: "/dev/mapper/os-root", FS: "xfs", Options: "defaults", UUID: "root-uuid"},
			{Mount: "/boot/efi", Device: "/dev/sda1", FS: "vfat", Options: "defaults", UUID: "esp-uuid"},
		},
	}
	body, _ := json.Marshal(res)
	resPath := filepath.Join(tmp, "storage-result.json")
	if err := os.WriteFile(resPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	stderr, rc := runEnv(t, "--result", resPath)
	if rc != 2 {
		t.Fatalf("grub on BIOS must exit 2 before dracut; got rc=%d stderr=%s", rc, stderr)
	}
	if !strings.Contains(stderr, "safety_refusal=firmware-not-uefi") {
		t.Fatalf("expected the firmware-not-uefi refusal; stderr=%s", stderr)
	}
}

// A disk root with boot_method=none (BIOS firmware) is the normal BIOS install:
// fstab is written for the real filesystems, no bootloader is touched, and the
// boot context records family=none.
func TestDiskRootBootloaderNoneOnBIOS(t *testing.T) {
	tmp := t.TempDir()
	sysroot := filepath.Join(tmp, "sysroot")
	if err := os.MkdirAll(sysroot, 0o750); err != nil {
		t.Fatal(err)
	}
	res := result.StorageResult{
		Version: 2, Name: "os", Role: "os",
		FirmwareMode: "bios", StorageType: "lvm",
		BootMethod: "none", BootFirmware: "uefi", RegisterNVRAM: "auto",
		RAIDMode: "none", MountedTargetRoot: sysroot,
		BootDevices: []string{"/dev/sda"},
		Filesystems: []result.Filesystem{
			{Mount: "/", Device: "/dev/mapper/os-root", FS: "xfs", Options: "defaults", UUID: "root-uuid"},
			{Mount: "/boot/efi", Device: "/dev/sda1", FS: "vfat", Options: "defaults", UUID: "esp-uuid"},
		},
	}
	body, _ := json.Marshal(res)
	resPath := filepath.Join(tmp, "storage-result.json")
	if err := os.WriteFile(resPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	bootCtxOut := filepath.Join(tmp, "boot-context.json")
	stderr, rc := runEnv(t, "--result", resPath, "--boot-context-out", bootCtxOut)
	if rc != 0 {
		t.Fatalf("a BIOS disk root must finalise cleanly; rc=%d stderr=%s", rc, stderr)
	}
	fstab, err := os.ReadFile(filepath.Join(sysroot, "etc", "fstab")) //nolint:gosec // test artefact
	if err != nil {
		t.Fatalf("fstab missing: %v", err)
	}
	for _, want := range []string{"UUID=root-uuid / xfs", "UUID=esp-uuid /boot/efi vfat"} {
		if !strings.Contains(string(fstab), want) {
			t.Fatalf("fstab missing %q:\n%s", want, fstab)
		}
	}
	bctx, err := os.ReadFile(bootCtxOut) //nolint:gosec // test artefact
	if err != nil {
		t.Fatalf("boot-context missing: %v", err)
	}
	if !strings.Contains(string(bctx), `"family": "none"`) {
		t.Fatalf("boot-context family wrong:\n%s", bctx)
	}
}

// readLog returns the tool's log file: UserInfo lines are mirrored there, so
// an operator-facing message is asserted from the log rather than from the
// error writer the test harness captures.
func readLog(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path) //nolint:gosec // test artefact
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// stageESPLoaders puts the loaders StageSignedShim writes onto a sysroot's ESP,
// so a none finalise has something to remove.
func stageESPLoaders(t *testing.T, sysroot string) {
	t.Helper()
	efi := filepath.Join(sysroot, "boot", "efi", "EFI")
	for dir, names := range map[string][]string{
		"rocky": {"shimx64.efi", "grubx64.efi", "grub.cfg", "grubenv"},
		"BOOT":  {"BOOTX64.EFI", "grubx64.efi"},
	} {
		if err := os.MkdirAll(filepath.Join(efi, dir), 0o750); err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			if err := os.WriteFile(filepath.Join(efi, dir, n), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// stageNoneResult writes a UEFI disk-OS storage result with boot_method=none —
// what lpart-storage-prepare emits for an OS set with bootloader: none.
func stageNoneResult(t *testing.T, tmp, sysroot string) string {
	t.Helper()
	res := result.StorageResult{
		Version: 2, Name: "os", Role: "os",
		FirmwareMode: "uefi", StorageType: "lvm",
		BootMethod: "none", BootOrder: "last", BootFirmware: "uefi", RegisterNVRAM: "auto",
		RAIDMode: "none", MountedTargetRoot: sysroot,
		BootDevices: []string{"/dev/sda"},
		Filesystems: []result.Filesystem{
			{Mount: "/", Device: "/dev/mapper/os-root", FS: "xfs", Options: "defaults", UUID: "root-uuid"},
			{Mount: "/boot/efi", Device: "/dev/sda1", FS: "vfat", Options: "defaults", UUID: "esp-uuid"},
		},
	}
	body, _ := json.Marshal(res)
	p := filepath.Join(tmp, "storage-result.json")
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A UEFI disk OS with bootloader: none writes fstab, installs nothing, and
// goes on to strip an existing bootloader -- otherwise a node switched from
// grub to none can still be firmware-booted from the stale install when PXE
// fails. Dry-run keeps the orchestrator test off the bind mounts and
// efibootmgr, so what it pins is the message and that the removal step is
// reached; internal/bootloader/remove_test.go pins what the step removes.
func TestDiskRootBootloaderNoneRemovesExistingSetup(t *testing.T) {
	tmp := t.TempDir()
	sysroot := filepath.Join(tmp, "sysroot")
	if err := os.MkdirAll(sysroot, 0o750); err != nil {
		t.Fatal(err)
	}
	stageESPLoaders(t, sysroot)
	resPath := stageNoneResult(t, tmp, sysroot)
	logPath := filepath.Join(tmp, "finalise.log")
	stderr, rc := runEnv(t, "--result", resPath, "--dry-run", "--log-file", logPath)
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, stderr)
	}
	log := readLog(t, logPath)
	if !strings.Contains(log, `bootloader "none"`) {
		t.Fatalf("the skip message must name the operator's choice, not the firmware:\n%s", log)
	}
	if !strings.Contains(log, "bootloader to remove") && !strings.Contains(log, "bootloader removed") {
		t.Fatalf("the none path must go on to remove an existing bootloader:\n%s", log)
	}
}

// On BIOS the none message names the firmware, and there is no UEFI boot path
// to strip: nothing on the ESP is touched and no efibootmgr runs.
func TestDiskRootBootloaderNoneOnBIOSRemovesNothing(t *testing.T) {
	tmp := t.TempDir()
	sysroot := filepath.Join(tmp, "sysroot")
	if err := os.MkdirAll(sysroot, 0o750); err != nil {
		t.Fatal(err)
	}
	stageESPLoaders(t, sysroot)
	res := result.StorageResult{
		Version: 2, Name: "os", Role: "os",
		FirmwareMode: "bios", StorageType: "lvm",
		BootMethod: "none", BootOrder: "last", BootFirmware: "uefi", RegisterNVRAM: "auto",
		RAIDMode: "none", MountedTargetRoot: sysroot,
		BootDevices: []string{"/dev/sda"},
		Filesystems: []result.Filesystem{
			{Mount: "/", Device: "/dev/mapper/os-root", FS: "xfs", Options: "defaults", UUID: "root-uuid"},
		},
	}
	body, _ := json.Marshal(res)
	resPath := filepath.Join(tmp, "storage-result.json")
	if err := os.WriteFile(resPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(tmp, "finalise.log")
	stderr, rc := runEnv(t, "--result", resPath, "--log-file", logPath)
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, stderr)
	}
	log := readLog(t, logPath)
	if !strings.Contains(log, `firmware is "bios"`) {
		t.Fatalf("the BIOS message must name the firmware as the reason:\n%s", log)
	}
	if _, err := os.Stat(filepath.Join(sysroot, "boot", "efi", "EFI", "BOOT", "BOOTX64.EFI")); err != nil {
		t.Errorf("a BIOS node has no UEFI boot path to strip: %v", err)
	}
}

// biosGRUBResult is what lpart-storage-prepare writes for a BIOS node whose os
// set asks for a bootloader. biosBootPartition 0 means the layout built none.
func biosGRUBResult(t *testing.T, biosBootPartition int) string {
	t.Helper()
	tmp := t.TempDir()
	sysroot := filepath.Join(tmp, "sysroot")
	if err := os.MkdirAll(filepath.Join(sysroot, "etc"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sysroot, "etc", "os-release"),
		[]byte("ID=rocky\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(result.StorageResult{
		Version: 2, Name: "os", Role: "os",
		FirmwareMode: "bios", StorageType: "lvm",
		BootMethod: "grub", BootOrder: "first", BootFirmware: "uefi", RegisterNVRAM: "auto",
		RAIDMode: "none", MountedTargetRoot: sysroot,
		BootDevices:       []string{"/dev/sda"},
		BIOSBootPartition: biosBootPartition,
		Filesystems: []result.Filesystem{
			{Mount: "/", Device: "/dev/mapper/os-root", FS: "xfs", Options: "defaults", UUID: "root-uuid"},
			{Mount: "/boot/efi", Device: "/dev/sda2", FS: "vfat", Options: "defaults", UUID: "esp-uuid"},
		},
	})
	resPath := filepath.Join(tmp, "storage-result.json")
	if err := os.WriteFile(resPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return resPath
}

// GRUB for BIOS refuses outright without a BIOS boot partition to hold its
// core image, so the refusal has to come before the bind mounts and dracut,
// with exit 2 (park) and the fix named: only a full install repartitions.
func TestBIOSGRUBWithoutBootPartitionRefused(t *testing.T) {
	stderr, rc := runEnv(t, "--result", biosGRUBResult(t, 0))
	if rc != 2 {
		t.Fatalf("missing BIOS boot partition must exit 2 before dracut; got rc=%d stderr=%s", rc, stderr)
	}
	if !strings.Contains(stderr, "safety_refusal=missing-bios-boot-partition") {
		t.Fatalf("expected the missing-bios-boot-partition refusal; stderr=%s", stderr)
	}
	for _, want := range []string{"install_mode", "full"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("refusal must name the fix (%q); stderr=%s", want, stderr)
		}
	}
}

// With the partition present the firmware gate lets the run through: it gets
// as far as the adapter rather than parking on either refusal.
func TestBIOSGRUBWithBootPartitionPassesTheGate(t *testing.T) {
	stderr, rc := runEnv(t, "--result", biosGRUBResult(t, 1))
	if rc == 2 {
		t.Fatalf("a BIOS GRUB install with its boot partition must not park; stderr=%s", stderr)
	}
	for _, unwanted := range []string{"safety_refusal=firmware-not-uefi", "safety_refusal=missing-bios-boot-partition"} {
		if strings.Contains(stderr, unwanted) {
			t.Fatalf("unexpected refusal %q; stderr=%s", unwanted, stderr)
		}
	}
}
