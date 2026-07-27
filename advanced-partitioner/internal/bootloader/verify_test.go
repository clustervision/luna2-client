package bootloader

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/result"
)

func writeTestFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// VerifyAndReport must actually prove the finalise: a registered NVRAM entry,
// the ESP fallback loader, and grub.cfg — failing loudly when any is absent.
func TestVerifyAndReport(t *testing.T) {
	logger, err := txlog.Init(txlog.Options{LogDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logger.Close() }()

	good := t.TempDir()
	writeTestFile(t, filepath.Join(good, "boot/efi/EFI/BOOT/BOOTX64.EFI"), "shim")
	writeTestFile(t, filepath.Join(good, "boot/grub/grub.cfg"), "set default=0")
	// efibootmgr lives in the installed image; verify reads NVRAM via
	// `chroot /sysroot efibootmgr`, so it must be present for the NVRAM check.
	writeTestFile(t, filepath.Join(good, "usr/sbin/efibootmgr"), "#!/bin/sh\n")
	bc := BootContext{Sysroot: good, Family: FamilyDebian, FirmwareMode: "uefi", Architecture: "x86_64"}

	withEntry := executor.NewFakeRunner(executor.FakeResponse{
		Stdout: []byte("BootCurrent: 0001\nBoot0001* TrinityX\tHD(1,GPT)/File(\\EFI\\BOOT\\BOOTX64.EFI)\n"),
	})
	noEntry := executor.NewFakeRunner(executor.FakeResponse{
		Stdout: []byte("BootCurrent: 0002\nBoot0002* EFI Network\n"),
	})

	// Happy path: NVRAM entry + ESP loader + grub.cfg all present.
	if err := VerifyAndReport(context.Background(), withEntry, logger, bc); err != nil {
		t.Fatalf("expected VERIFIED, got: %v", err)
	}

	// No TrinityX entry in NVRAM -> failure even though ESP+grub exist.
	if err := VerifyAndReport(context.Background(), noEntry, logger, bc); err == nil {
		t.Fatal("expected failure when NVRAM entry missing")
	}

	// Missing ESP fallback loader -> failure.
	noLoader := t.TempDir()
	writeTestFile(t, filepath.Join(noLoader, "boot/grub/grub.cfg"), "x")
	writeTestFile(t, filepath.Join(noLoader, "usr/sbin/efibootmgr"), "#!/bin/sh\n")
	bc2 := BootContext{Sysroot: noLoader, Family: FamilyDebian, FirmwareMode: "uefi", Architecture: "x86_64"}
	if err := VerifyAndReport(context.Background(), withEntry, logger, bc2); err == nil {
		t.Fatal("expected failure when ESP fallback loader missing")
	}

	// No efibootmgr in the image -> NVRAM check is skipped (not a hard failure),
	// so an otherwise-complete finalise still verifies. Guards the real-UEFI bug
	// where the host initramfs has no efibootmgr and the read must not fail the
	// install. ESP loader + grub are present here.
	noTool := t.TempDir()
	writeTestFile(t, filepath.Join(noTool, "boot/efi/EFI/BOOT/BOOTX64.EFI"), "shim")
	writeTestFile(t, filepath.Join(noTool, "boot/grub/grub.cfg"), "x")
	bc3 := BootContext{Sysroot: noTool, Family: FamilyDebian, FirmwareMode: "uefi", Architecture: "x86_64"}
	if err := VerifyAndReport(context.Background(), noEntry, logger, bc3); err != nil {
		t.Fatalf("missing efibootmgr should skip the NVRAM check, not fail: %v", err)
	}

	// memboot / RAM root (FamilyNone): no bootloader to verify -> nil.
	if err := VerifyAndReport(context.Background(), withEntry, logger, BootContext{Family: FamilyNone}); err != nil {
		t.Fatalf("FamilyNone should verify as nil, got: %v", err)
	}
}

// A MULTI-disk (mirrored-boot) install without efibootmgr in the image must
// FAIL verification: the per-leg NVRAM entries are the verified failover
// property, and skipping the check silently would report success for an
// install whose disk-loss failover does not exist ("redundancy theatre").
// Single-disk keeps the tolerant skip; register_nvram=no is the opt-out.
// FTA v2 BE-W3 / MCS-17, barrier NB-5.
func TestVerifyRedundantBootRequiresNVRAMTooling(t *testing.T) {
	logger, err := txlog.Init(txlog.Options{LogDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logger.Close() }()

	sysroot := t.TempDir()
	writeTestFile(t, filepath.Join(sysroot, "boot/efi/EFI/BOOT/BOOTX64.EFI"), "shim")
	writeTestFile(t, filepath.Join(sysroot, "boot/grub/grub.cfg"), "x")
	// NO usr/sbin/efibootmgr in the image.
	runner := executor.NewFakeRunner(executor.FakeResponse{})

	multi := BootContext{
		Sysroot: sysroot, Family: FamilyDebian, FirmwareMode: "uefi", Architecture: "x86_64",
		StorageResult: result.StorageResult{BootDevices: []string{"/dev/vda", "/dev/vdb"}},
	}
	if err := VerifyAndReport(context.Background(), runner, logger, multi); err == nil {
		t.Fatal("multi-disk mirrored boot without efibootmgr must FAIL verification")
	}

	// register_nvram=no is the deliberate opt-out: same shape passes.
	optOut := multi
	optOut.RegisterNVRAM = "no"
	if err := VerifyAndReport(context.Background(), runner, logger, optOut); err != nil {
		t.Fatalf("register_nvram=no must accept fallback-only boot: %v", err)
	}

	// Single boot disk: tolerant skip preserved.
	single := multi
	single.StorageResult = result.StorageResult{BootDevices: []string{"/dev/vda"}}
	if err := VerifyAndReport(context.Background(), runner, logger, single); err != nil {
		t.Fatalf("single-disk install without efibootmgr must still verify: %v", err)
	}
}
