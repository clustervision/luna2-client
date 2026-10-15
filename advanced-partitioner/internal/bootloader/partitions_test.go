package bootloader

import (
	"context"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// lsblkRunner answers `lsblk ... <disk>` with out and everything else with def.
func lsblkRunner(out string, def executor.FakeResponse) *executor.FakeRunner {
	f := executor.NewFakeRunner(def)
	f.AddMatch(executor.FakeMatch{Path: "lsblk", Response: executor.FakeResponse{Stdout: []byte(out)}})
	return f
}

// An empty PARTTYPE (no udev database) is probed with blkid -p.
func TestListBlockDevsProbesMissingPartType(t *testing.T) {
	f := lsblkRunner(`NAME="/dev/sda1" TYPE="part" PARTTYPE="" PKNAME="/dev/sda" MOUNTPOINT=""`+"\n", executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "blkid", Response: executor.FakeResponse{Stdout: []byte("21686148-6449-6e6f-744e-656564454649\n")}})
	if !HasBIOSBootPartition(context.Background(), f, "/dev/sda") {
		t.Fatal("blkid-probed EF02 partition not found")
	}
}

// esp_partition 0 in the storage result means partition 1; the disks are not
// probed for it.
func TestRegisterUEFIEntriesAssumesPartition1(t *testing.T) {
	sysroot := mkEFISysroot(t)
	f := lsblkRunner(`NAME="/dev/sda2" TYPE="part" PARTTYPE="c12a7328-f81f-11d2-ba4b-00a0c93ec93b" PKNAME="/dev/sda" MOUNTPOINT="/sysroot/boot/efi"`+"\n",
		executor.FakeResponse{Stdout: []byte("BootOrder: 0010\nBoot0010* UEFI: PXE IP4\n")})
	if err := RegisterUEFIEntries(context.Background(), RegisterUEFIEntriesOptions{
		Runner: f, Sysroot: sysroot, Arch: "x86_64", BootDevices: []string{"/dev/sda"}, RegisterNVRAM: "auto",
	}); err != nil {
		t.Fatalf("RegisterUEFIEntries: %v", err)
	}
	created := false
	for _, c := range f.Calls() {
		if c.Path == "lsblk" || c.Path == "blkid" {
			t.Fatalf("the disk was probed: %s %v", c.Path, c.Args)
		}
		for i, a := range c.Args {
			if a == "--part" {
				created = true
				if c.Args[i+1] != "1" {
					t.Fatalf("--part %s, want 1", c.Args[i+1])
				}
			}
		}
	}
	if !created {
		t.Fatal("no efibootmgr --create")
	}
}
