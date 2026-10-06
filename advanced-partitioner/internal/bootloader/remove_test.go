package bootloader

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fsops"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
)

// mkStagedESP builds a sysroot whose ESP holds what StageSignedShim writes,
// plus the files that must survive: the stub configs, grubenv and the fonts
// directory a distro ships.
func mkStagedESP(t *testing.T) string {
	t.Helper()
	sysroot := mkEFISysroot(t)
	efi := filepath.Join(sysroot, "boot", "efi", "EFI")
	for dir, names := range map[string][]string{
		"rocky": {"shimx64.efi", "grubx64.efi", "grub.efi", "grub.cfg", "grubenv", "BOOTX64.CSV"},
		"BOOT":  {"BOOTX64.EFI", "grubx64.efi", "grub.efi", "grub.cfg"},
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
	if err := os.MkdirAll(filepath.Join(efi, "rocky", "fonts"), 0o750); err != nil {
		t.Fatal(err)
	}
	return sysroot
}

func removeOpts(t *testing.T, sysroot string, runner executor.Runner) (RemoveExistingBootloaderOptions, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "finalise.log")
	logger, err := txlog.Init(txlog.Options{LogFile: logPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	return RemoveExistingBootloaderOptions{
		Runner:  runner,
		Logger:  logger,
		FS:      fsops.FS{},
		Sysroot: sysroot,
		Arch:    "x86_64",
	}, logPath
}

// Switching a node to bootloader=none must take the old local boot path away,
// but only ours: the network entries are how the controller keeps control and
// another OS's manager is not ours to delete.
func TestRemoveExistingBootloaderDeletesOnlyTrinityNVRAM(t *testing.T) {
	sysroot := mkStagedESP(t)
	listing := `BootOrder: 0010,0001,0002,0003
Boot0001* TrinityX sda
Boot0002* TrinityX sdb
Boot0003* Windows Boot Manager
Boot0010* UEFI: PXE IP4 Intel(R) Ethernet
`
	f := executor.NewFakeRunner(executor.FakeResponse{Stdout: []byte(listing)})
	opts, _ := removeOpts(t, sysroot, f)
	if err := RemoveExistingBootloader(context.Background(), opts); err != nil {
		t.Fatalf("RemoveExistingBootloader: %v", err)
	}
	deleted := map[string]bool{}
	for _, c := range f.Calls() {
		if len(c.Args) == 3 && c.Args[0] == "--bootnum" && c.Args[2] == "--delete-bootnum" {
			deleted[c.Args[1]] = true
		}
	}
	for _, num := range []string{"0001", "0002"} {
		if !deleted[num] {
			t.Errorf("TrinityX entry Boot%s not deleted; deleted=%v", num, deleted)
		}
	}
	for _, num := range []string{"0003", "0010"} {
		if deleted[num] {
			t.Errorf("Boot%s is not ours to delete", num)
		}
	}
}

// The loaders go; everything else on the ESP stays, including the stub
// configs, grubenv and the distro's own directories.
func TestRemoveExistingBootloaderStripsESPLoaders(t *testing.T) {
	sysroot := mkStagedESP(t)
	f := executor.NewFakeRunner(executor.FakeResponse{Stdout: []byte("BootOrder: 0010\nBoot0010* UEFI: PXE IP4\n")})
	opts, logPath := removeOpts(t, sysroot, f)
	if err := RemoveExistingBootloader(context.Background(), opts); err != nil {
		t.Fatalf("RemoveExistingBootloader: %v", err)
	}
	efi := filepath.Join(sysroot, "boot", "efi", "EFI")
	gone := []string{
		"rocky/shimx64.efi", "rocky/grubx64.efi", "rocky/grub.efi",
		"BOOT/BOOTX64.EFI", "BOOT/grubx64.efi", "BOOT/grub.efi",
	}
	for _, rel := range gone {
		if _, err := os.Stat(filepath.Join(efi, rel)); err == nil {
			t.Errorf("%s must be removed: a node that still has a loader can still be firmware-booted", rel)
		}
	}
	kept := []string{"rocky/grub.cfg", "rocky/grubenv", "rocky/BOOTX64.CSV", "rocky/fonts", "BOOT/grub.cfg"}
	for _, rel := range kept {
		if _, err := os.Stat(filepath.Join(efi, rel)); err != nil {
			t.Errorf("%s must be left alone: %v", rel, err)
		}
	}
	body, err := os.ReadFile(logPath) //nolint:gosec // test artefact
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "existing bootloader removed") {
		t.Errorf("the removal must be stated in one summary line:\n%s", body)
	}
}

// The osimage extraction re-creates the ESP directories on every sync, so this
// runs on every none finalise: a second pass with nothing left must succeed
// and say so rather than erroring on the missing files.
func TestRemoveExistingBootloaderIsIdempotent(t *testing.T) {
	sysroot := mkStagedESP(t)
	f := executor.NewFakeRunner(executor.FakeResponse{Stdout: []byte("BootOrder: 0010\nBoot0010* UEFI: PXE IP4\n")})
	opts, _ := removeOpts(t, sysroot, f)
	if err := RemoveExistingBootloader(context.Background(), opts); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	opts2, logPath := removeOpts(t, sysroot, f)
	if err := RemoveExistingBootloader(context.Background(), opts2); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	body, err := os.ReadFile(logPath) //nolint:gosec // test artefact
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "no existing bootloader to remove") {
		t.Errorf("a no-op pass must say so:\n%s", body)
	}
}

// No efivars under the sysroot means the firmware is not UEFI or the mounts
// are missing: the NVRAM half is skipped LOUDLY (the same skip
// RegisterUEFIEntries makes) and the ESP half still runs.
func TestRemoveExistingBootloaderSkipsNVRAMWithoutEfivars(t *testing.T) {
	sysroot := mkStagedESP(t)
	if err := os.RemoveAll(filepath.Join(sysroot, "sys", "firmware", "efi", "efivars")); err != nil {
		t.Fatal(err)
	}
	f := executor.NewFakeRunner(executor.FakeResponse{Stdout: []byte("")})
	opts, logPath := removeOpts(t, sysroot, f)
	if err := RemoveExistingBootloader(context.Background(), opts); err != nil {
		t.Fatalf("RemoveExistingBootloader: %v", err)
	}
	if len(f.Calls()) != 0 {
		t.Errorf("no efibootmgr call is possible without efivars: %v", f.Calls())
	}
	if _, err := os.Stat(filepath.Join(sysroot, "boot", "efi", "EFI", "BOOT", "BOOTX64.EFI")); err == nil {
		t.Error("the ESP loaders must still be removed")
	}
	body, err := os.ReadFile(logPath) //nolint:gosec // test artefact
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "level=WARN") {
		t.Errorf("the NVRAM skip must be loud:\n%s", body)
	}
}
