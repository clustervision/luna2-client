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

func TestDetectESPPartition(t *testing.T) {
	for _, tc := range []struct {
		name, disk, espDev, lsblk string
		want                      int
	}{
		{"single EF00, not partition 1", "/dev/sda", "", `NAME="/dev/sda" TYPE="disk" PARTTYPE="" PKNAME="" MOUNTPOINT=""
NAME="/dev/sda1" TYPE="part" PARTTYPE="21686148-6449-6e6f-744e-656564454649" PKNAME="/dev/sda" MOUNTPOINT=""
NAME="/dev/sda2" TYPE="part" PARTTYPE="C12A7328-F81F-11D2-BA4B-00A0C93EC93B" PKNAME="/dev/sda" MOUNTPOINT=""
NAME="/dev/sda3" TYPE="part" PARTTYPE="0fc63daf-8483-4772-8e79-3d69d8477de4" PKNAME="/dev/sda" MOUNTPOINT="/sysroot"
`, 2},
		{"nvme naming", "/dev/nvme0n1", "", `NAME="/dev/nvme0n1" TYPE="disk" PARTTYPE="" PKNAME="" MOUNTPOINT=""
NAME="/dev/nvme0n1p1" TYPE="part" PARTTYPE="c12a7328-f81f-11d2-ba4b-00a0c93ec93b" PKNAME="/dev/nvme0n1" MOUNTPOINT="/sysroot/boot/efi"
`, 1},
		{"raid members typed FD00: the one under the mounted md mirror", "/dev/sdb", "/dev/md127", `NAME="/dev/sdb" TYPE="disk" PARTTYPE="" PKNAME="" MOUNTPOINT=""
NAME="/dev/sdb1" TYPE="part" PARTTYPE="a19d880f-05fc-4d3b-a006-743f0f84911e" PKNAME="/dev/sdb" MOUNTPOINT=""
NAME="/dev/sdb3" TYPE="part" PARTTYPE="a19d880f-05fc-4d3b-a006-743f0f84911e" PKNAME="/dev/sdb" MOUNTPOINT=""
NAME="/dev/md126" TYPE="raid1" PARTTYPE="" PKNAME="/dev/sdb1" MOUNTPOINT="/sysroot/boot"
NAME="/dev/md127" TYPE="raid1" PARTTYPE="" PKNAME="/dev/sdb3" MOUNTPOINT="/sysroot/boot/efi"
`, 3},
		{"two EF00: the one holding /boot/efi", "/dev/sda", "/dev/sda4", `NAME="/dev/sda1" TYPE="part" PARTTYPE="c12a7328-f81f-11d2-ba4b-00a0c93ec93b" PKNAME="/dev/sda" MOUNTPOINT=""
NAME="/dev/sda4" TYPE="part" PARTTYPE="c12a7328-f81f-11d2-ba4b-00a0c93ec93b" PKNAME="/dev/sda" MOUNTPOINT=""
`, 4},
		{"MBR label 0xef", "/dev/vda", "", `NAME="/dev/vda1" TYPE="part" PARTTYPE="0x83" PKNAME="/dev/vda" MOUNTPOINT=""
NAME="/dev/vda2" TYPE="part" PARTTYPE="0xef" PKNAME="/dev/vda" MOUNTPOINT=""
`, 2},
		{"nothing to go on", "/dev/sda", "", `NAME="/dev/sda" TYPE="disk" PARTTYPE="" PKNAME="" MOUNTPOINT=""`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := lsblkRunner(tc.lsblk, executor.FakeResponse{})
			if got := detectESPPartition(context.Background(), f, tc.disk, tc.espDev); got != tc.want {
				t.Fatalf("detectESPPartition = %d, want %d", got, tc.want)
			}
		})
	}
}

// An empty PARTTYPE (no udev database) is probed with blkid -p.
func TestListBlockDevsProbesMissingPartType(t *testing.T) {
	f := lsblkRunner(`NAME="/dev/sda1" TYPE="part" PARTTYPE="" PKNAME="/dev/sda" MOUNTPOINT=""`+"\n", executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "blkid", Response: executor.FakeResponse{Stdout: []byte("21686148-6449-6e6f-744e-656564454649\n")}})
	if !HasBIOSBootPartition(context.Background(), f, "/dev/sda") {
		t.Fatal("blkid-probed EF02 partition not found")
	}
}

// esp_partition 0 in the storage result (mount-existing): the efibootmgr entry
// names the partition actually on the disk, not a guessed 1.
func TestRegisterUEFIEntriesDetectsESPPartition(t *testing.T) {
	sysroot := mkEFISysroot(t)
	f := lsblkRunner(`NAME="/dev/sda2" TYPE="part" PARTTYPE="c12a7328-f81f-11d2-ba4b-00a0c93ec93b" PKNAME="/dev/sda" MOUNTPOINT="/sysroot/boot/efi"`+"\n",
		executor.FakeResponse{Stdout: []byte("BootOrder: 0010\nBoot0010* UEFI: PXE IP4\n")})
	if err := RegisterUEFIEntries(context.Background(), RegisterUEFIEntriesOptions{
		Runner: f, Sysroot: sysroot, Arch: "x86_64", BootDevices: []string{"/dev/sda"}, RegisterNVRAM: "auto",
	}); err != nil {
		t.Fatalf("RegisterUEFIEntries: %v", err)
	}
	for _, c := range f.Calls() {
		for i, a := range c.Args {
			if a == "--part" {
				if c.Args[i+1] != "2" {
					t.Fatalf("--part %s, want the detected 2", c.Args[i+1])
				}
				return
			}
		}
	}
	t.Fatal("no efibootmgr --create")
}
