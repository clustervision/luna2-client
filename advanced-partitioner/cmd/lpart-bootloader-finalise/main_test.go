package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/bootloader"
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
		// never a real disk: the BIOS none path clears the MBR boot code
		BootDevices: []string{biosDiskImage(t)},
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

// biosDiskImage writes a disk image whose MBR carries boot code, as an earlier
// bootloader "first" install leaves it, and returns its path.
func biosDiskImage(t *testing.T) string {
	t.Helper()
	img := make([]byte, 4096)
	for i := 0; i < 440; i++ {
		img[i] = 0xEB
	}
	for i := 446; i < 510; i++ {
		img[i] = 0x11 // partition table stand-in
	}
	img[510], img[511] = 0x55, 0xAA
	p := filepath.Join(t.TempDir(), "disk.img")
	if err := os.WriteFile(p, img, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// biosNoneRun finalises a BIOS disk root whose MBR carries boot code, with the
// given boot_order, and returns the log, the disk image and its first sector.
func biosNoneRun(t *testing.T, bootOrder string) (log string, disk string, before []byte) {
	t.Helper()
	tmp := t.TempDir()
	sysroot := filepath.Join(tmp, "sysroot")
	if err := os.MkdirAll(sysroot, 0o750); err != nil {
		t.Fatal(err)
	}
	stageESPLoaders(t, sysroot)
	disk = biosDiskImage(t)
	before, err := os.ReadFile(disk) //nolint:gosec // test artefact
	if err != nil {
		t.Fatal(err)
	}
	res := result.StorageResult{
		Version: 2, Name: "os", Role: "os",
		FirmwareMode: "bios", StorageType: "lvm",
		BootMethod: "none", BootOrder: bootOrder, BootFirmware: "uefi", RegisterNVRAM: "auto",
		RAIDMode: "none", MountedTargetRoot: sysroot,
		BootDevices: []string{disk},
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
	log = readLog(t, logPath)
	if !strings.Contains(log, `firmware is "bios"`) {
		t.Fatalf("the BIOS message must name the firmware:\n%s", log)
	}
	if _, err := os.Stat(filepath.Join(sysroot, "boot", "efi", "EFI", "BOOT", "BOOTX64.EFI")); err != nil {
		t.Errorf("a BIOS node has no UEFI boot path to strip: %v", err)
	}
	return log, disk, before
}

// "last" on BIOS installs nothing and keeps what an earlier "first" left on the
// disk: it is the fallback when PXE fails.
func TestDiskRootBootloaderLastOnBIOSKeepsBootCode(t *testing.T) {
	log, disk, before := biosNoneRun(t, "last")
	if !strings.Contains(log, "fallback behind PXE") {
		t.Errorf("the last message must say the disk's GRUB stays:\n%s", log)
	}
	after, err := os.ReadFile(disk) //nolint:gosec // test artefact
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal(`bootloader "last" must not touch the disk`)
	}
}

// "none" on BIOS goes through the removal (proven against disk images in the
// bootloader package); here the disk cannot be listed, so it is warned about and
// left rather than half-cleared.
func TestDiskRootBootloaderNoneOnBIOSRunsTheRemoval(t *testing.T) {
	log, _, _ := biosNoneRun(t, "none")
	for _, want := range []string{`asks for bootloader "none"`, "left as it is"} {
		if !strings.Contains(log, want) {
			t.Errorf("missing %q:\n%s", want, log)
		}
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
		// never a real disk: the gate must refuse before anything reaches it
		BootDevices:       []string{filepath.Join(tmp, "no-such-disk")},
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

// classSysroot stages a sysroot with the given os-release and image files and
// returns it with a storage-result path describing a grub install on it.
func classSysroot(t *testing.T, osRelease string, files []string, mutate func(*result.StorageResult)) (string, string) {
	t.Helper()
	tmp := t.TempDir()
	sysroot := filepath.Join(tmp, "sysroot")
	for _, rel := range append([]string{"etc/os-release"}, files...) {
		p := filepath.Join(sysroot, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		body := ""
		if rel == "etc/os-release" {
			body = osRelease
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(sysroot, "boot", "efi"), 0o750); err != nil {
		t.Fatal(err)
	}
	res := result.StorageResult{
		Version: 2, Name: "os", Role: "os",
		FirmwareMode: "uefi", StorageType: "lvm",
		BootMethod: "grub", BootOrder: "last", BootFirmware: "uefi", RegisterNVRAM: "auto",
		RAIDMode: "none", MountedTargetRoot: sysroot,
		BootDevices: []string{filepath.Join(tmp, "no-such-disk")},
		Filesystems: []result.Filesystem{
			{Mount: "/", Device: "/dev/mapper/os-root", FS: "xfs", UUID: "root-uuid"},
			{Mount: "/boot", Device: "/dev/sda2", FS: "xfs", UUID: "boot-uuid"},
			{Mount: "/boot/efi", Device: "/dev/sda1", FS: "vfat", UUID: "esp-uuid"},
		},
	}
	if mutate != nil {
		mutate(&res)
	}
	body, _ := json.Marshal(res)
	p := filepath.Join(tmp, "storage-result.json")
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return sysroot, p
}

var rhelTools = []string{"usr/sbin/dracut", "usr/sbin/grub2-mkconfig"}

// Review finding 9: deterministic failures park (exit 3, fatal_config=<reason>)
// instead of exit 1, which the wrapper retries every 30 s for ever -- in full
// mode re-wiping the disks and downloading the image each time. None of these
// may reach the bind mounts (non-dry-run: a mount attempt as a normal user
// would turn them into exit 1).
func TestDeterministicFailuresExit3(t *testing.T) {
	for _, tc := range []struct {
		name, osRelease string
		files           []string
		mutate          func(*result.StorageResult)
		reason          string
	}{
		{"unsupported OS family", "ID=gentoo\n", nil, nil, "unsupported-os-family"},
		{"image without dracut", "ID=rocky\n", []string{"usr/sbin/grub2-mkconfig"}, nil, "image-missing-tool"},
		{"image without grub2-mkconfig", "ID=rocky\n", []string{"usr/sbin/dracut"}, nil, "image-missing-tool"},
		{"debian image without update-grub", "ID=ubuntu\n", []string{"usr/sbin/update-initramfs"}, nil, "image-missing-tool"},
		{"fstab entry without UUID", "ID=rocky\n", rhelTools, func(r *result.StorageResult) {
			r.Filesystems[1].UUID = ""
		}, "fstab-missing-uuid"},
		{"no boot devices", "ID=rocky\n", rhelTools, func(r *result.StorageResult) {
			r.BootDevices = []string{}
		}, "no-boot-devices"},
		{"BIOS first without grub2-install", "ID=rocky\n", rhelTools, func(r *result.StorageResult) {
			r.FirmwareMode, r.BootOrder, r.BIOSBootPartition = "bios", "first", 1
		}, "image-missing-bios-grub"},
		{"BIOS first without the i386-pc modules", "ID=rocky\n", append([]string{"usr/sbin/grub2-install"}, rhelTools...), func(r *result.StorageResult) {
			r.FirmwareMode, r.BootOrder, r.BIOSBootPartition = "bios", "first", 1
		}, "image-missing-bios-grub"},
		{"debian BIOS first without grub-install", "ID=ubuntu\n", []string{"usr/sbin/update-initramfs", "usr/sbin/update-grub"}, func(r *result.StorageResult) {
			r.FirmwareMode, r.BootOrder, r.BIOSBootPartition = "bios", "first", 1
		}, "image-missing-bios-grub"},
		{"mirrored boot without efibootmgr", "ID=rocky\n", rhelTools, func(r *result.StorageResult) {
			r.BootDevices = append(r.BootDevices, r.BootDevices[0]+"-b")
		}, "image-missing-efibootmgr"},
		{"clear_uefi_nvram without efibootmgr", "ID=rocky\n", rhelTools, func(r *result.StorageResult) {
			r.ClearUEFINVRAM = true
		}, "image-missing-efibootmgr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, resPath := classSysroot(t, tc.osRelease, tc.files, tc.mutate)
			stderr, rc := runEnv(t, "--result", resPath)
			if rc != 3 || !strings.Contains(stderr, "fatal_config="+tc.reason) {
				t.Fatalf("want exit 3 fatal_config=%s; rc=%d stderr=%s", tc.reason, rc, stderr)
			}
		})
	}
}

// A dry run on a fresh disk previews the install: it writes nothing, so it
// must not then fail validating the boot files it did not write.
func TestDryRunOnFreshDiskSucceeds(t *testing.T) {
	files := append([]string{"lib/modules/5.14.0-1.el9.x86_64/vmlinuz"}, rhelTools...)
	sysroot, resPath := classSysroot(t, "ID=rocky\n", files, nil)
	stageESPLoaders(t, sysroot)
	stderr, rc := runEnv(t, "--result", resPath, "--dry-run")
	if rc != 0 {
		t.Fatalf("dry run on a fresh disk: rc=%d stderr=%s", rc, stderr)
	}
}

// Two os sets cannot both be booted: the result is invalid (exit 3), not
// silently reduced to the last one.
func TestTwoOSSetsInStorageResultExit3(t *testing.T) {
	p := filepath.Join(t.TempDir(), "storage-result.json")
	body := `{"version":2,"sets":[{"name":"os","role":"os"},{"name":"os2","role":"os"}]}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr, rc := runEnv(t, "--result", p)
	if rc != 3 || !strings.Contains(stderr, "fatal_config=storage-result-invalid more than one os set") {
		t.Fatalf("rc=%d stderr=%s", rc, stderr)
	}
}

func TestInvalidStorageResultExit3(t *testing.T) {
	p := filepath.Join(t.TempDir(), "storage-result.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr, rc := runEnv(t, "--result", p)
	if rc != 3 || !strings.Contains(stderr, "fatal_config=storage-result-invalid") {
		t.Fatalf("rc=%d stderr=%s", rc, stderr)
	}
	_, resPath := classSysroot(t, "ID=rocky\n", rhelTools, nil)
	sysroot := filepath.Dir(resPath) + "/sysroot"
	if err := os.Remove(filepath.Join(sysroot, "etc", "os-release")); err != nil {
		t.Fatal(err)
	}
	if stderr, rc := runEnv(t, "--result", resPath); rc != 3 || !strings.Contains(stderr, "fatal_config=image-missing-os-release") {
		t.Fatalf("missing os-release: rc=%d stderr=%s", rc, stderr)
	}
}

// fail maps the typed error, not the message: a transient command failure is
// a retry (exit 1) whatever it says.
func TestFailClassification(t *testing.T) {
	var buf bytes.Buffer
	if rc := fail(&buf, "", errors.New("efibootmgr --create /dev/sda: exit status 5 (missing UUID)")); rc != 1 {
		t.Fatalf("transient failure: rc=%d", rc)
	}
	wrapped := fmt.Errorf("StageSignedShim: %w", &bootloader.FatalError{Kind: "image", Reason: "image-no-signed-shim", Err: errors.New("no shim")})
	buf.Reset()
	if rc := fail(&buf, "", wrapped); rc != 3 || !strings.Contains(buf.String(), "fatal_config=image-no-signed-shim") {
		t.Fatalf("wrapped FatalError: rc=%d out=%s", rc, buf.String())
	}
}

// A bad command line is the same on every attempt: exit 3 parks with a
// fatal_config line, as in every lpart tool; exit 2 is reserved for safety
// refusals.
func TestBadFlagIsFatalConfig(t *testing.T) {
	var out, errb bytes.Buffer
	rc := run([]string{toolName, "--no-such-flag"}, &out, &errb)
	if rc != 3 || !strings.Contains(errb.String(), "fatal_config=usage") {
		t.Fatalf("rc=%d (want 3), stderr=%s", rc, errb.String())
	}
}
