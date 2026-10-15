package bootloader

import (
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fsops"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/result"
)

func TestWriteFstabBasic(t *testing.T) {
	sysroot := t.TempDir()
	sr := result.StorageResult{
		Filesystems: []result.Filesystem{
			{Mount: "/", FS: "xfs", Device: "/dev/mapper/os-root", UUID: "root-uuid"},
			{Mount: "/boot", FS: "xfs", Device: "/dev/sda2", UUID: "boot-uuid"},
			{Mount: "/boot/efi", FS: "vfat", Device: "/dev/sda1", UUID: "efi-uuid"},
			{Mount: "swap", FS: "swap", Device: "/dev/mapper/os-swap", UUID: "swap-uuid"},
		},
	}
	if err := WriteFstab(fsops.FS{}, nil, sysroot, sr); err != nil {
		t.Fatalf("WriteFstab: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(sysroot, "etc", "fstab")) //nolint:gosec // test artefact
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"UUID=root-uuid / xfs defaults 0 0",
		"UUID=boot-uuid /boot xfs defaults 0 0",
		"UUID=efi-uuid /boot/efi vfat umask=0077,shortname=winnt 0 0",
		"UUID=swap-uuid none swap defaults 0 0",
	} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("fstab missing %q:\n%s", want, body)
		}
	}
}

func TestWriteFstabExt4DumpPass(t *testing.T) {
	sysroot := t.TempDir()
	sr := result.StorageResult{
		Filesystems: []result.Filesystem{
			{Mount: "/", FS: "ext4", Device: "/dev/sda1", UUID: "root-ext4"},
			{Mount: "/home", FS: "ext4", Device: "/dev/sda2", UUID: "home-ext4"},
		},
	}
	if err := WriteFstab(fsops.FS{}, nil, sysroot, sr); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(sysroot, "etc", "fstab")) //nolint:gosec // test artefact
	// Root gets dump=1 pass=1; others dump=1 pass=2.
	if !strings.Contains(string(body), "UUID=root-ext4 / ext4 defaults 1 1") {
		t.Fatalf("ext4 root dump/pass wrong:\n%s", body)
	}
	if !strings.Contains(string(body), "UUID=home-ext4 /home ext4 defaults 1 2") {
		t.Fatalf("ext4 non-root dump/pass wrong:\n%s", body)
	}
}

func TestWriteFstabTmpfsRoot(t *testing.T) {
	sysroot := t.TempDir()
	sr := result.StorageResult{
		Filesystems: []result.Filesystem{
			{Mount: "/", FS: "tmpfs", Device: "tmpfs", Options: "defaults,size=2G"},
		},
	}
	if err := WriteFstab(fsops.FS{}, nil, sysroot, sr); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(sysroot, "etc", "fstab")) //nolint:gosec // test artefact
	if !strings.Contains(string(body), "tmpfs / tmpfs defaults,size=2G 0 0") {
		t.Fatalf("tmpfs line wrong:\n%s", body)
	}
}

func TestWriteFstabPreservesExisting(t *testing.T) {
	sysroot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sysroot, "etc"), 0o750); err != nil {
		t.Fatal(err)
	}
	existing := "" +
		"# preserved comment\n" +
		"UUID=existing-uuid / xfs defaults 0 0\n"
	if err := os.WriteFile(filepath.Join(sysroot, "etc", "fstab"), []byte(existing), 0o644); err != nil { //nolint:gosec // test artefact
		t.Fatal(err)
	}
	sr := result.StorageResult{
		Filesystems: []result.Filesystem{
			{Mount: "/", FS: "xfs", Device: "/dev/sda1", UUID: "new-uuid"},
			{Mount: "/boot", FS: "xfs", Device: "/dev/sda2", UUID: "boot-uuid"},
		},
	}
	if err := WriteFstab(fsops.FS{}, nil, sysroot, sr); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(sysroot, "etc", "fstab")) //nolint:gosec // test artefact
	s := string(body)
	if !strings.Contains(s, "# preserved comment") {
		t.Fatalf("comment lost:\n%s", s)
	}
	// The / mount is OURS this install: the stale existing entry is
	// REPLACED by the node's real UUID (ancestor FTA MCS-6 — the old
	// skip semantics preserved a golden image's stale root entry and the
	// node dropped to the emergency shell at boot).
	if strings.Contains(s, "UUID=existing-uuid") {
		t.Fatalf("stale / entry must be replaced by the node's:\n%s", s)
	}
	if !strings.Contains(s, "UUID=new-uuid / xfs") {
		t.Fatalf("node / entry missing:\n%s", s)
	}
	// /boot is new and should be appended.
	if !strings.Contains(s, "UUID=boot-uuid /boot xfs") {
		t.Fatalf("/boot not appended:\n%s", s)
	}
}

// A filesystem without a UUID in the result is read back with blkid after
// udev settled; when blkid has none either, the entry is fatal.
func TestWriteFstabMissingUUID(t *testing.T) {
	sr := result.StorageResult{
		Filesystems: []result.Filesystem{
			{Mount: "/", FS: "xfs", Device: "/dev/sda1"}, // no UUID
		},
	}
	f := executor.NewFakeRunner(executor.FakeResponse{})
	err := WriteFstab(fsops.FS{}, f, t.TempDir(), sr)
	if err == nil || !strings.Contains(err.Error(), "missing UUID") {
		t.Fatalf("expected missing-UUID error; got %v", err)
	}

	sysroot := t.TempDir()
	f = executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "blkid", ArgsPrefix: []string{"-p", "-s", "UUID", "-o", "value", "/dev/sda1"},
		Response: executor.FakeResponse{Stdout: []byte("late-uuid\n")}})
	if err := WriteFstab(fsops.FS{}, f, sysroot, sr); err != nil {
		t.Fatalf("WriteFstab: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(sysroot, "etc", "fstab")) //nolint:gosec // test artefact
	if !strings.Contains(string(body), "UUID=late-uuid / xfs defaults 0 0") {
		t.Fatalf("recovered UUID not used:\n%s", body)
	}
	calls := f.Calls()
	if len(calls) != 2 || calls[0].Path != "udevadm" || calls[0].Args[0] != "settle" || calls[1].Path != "blkid" {
		t.Fatalf("expected udevadm settle then blkid, got %v", calls)
	}
}

func TestFstabAlreadyHasMountWordBoundary(t *testing.T) {
	existing := []string{
		"UUID=u / xfs defaults 0 0",
		"UUID=u /boot xfs defaults 0 0",
	}
	if !fstabAlreadyHasMount(existing, "/boot") {
		t.Fatal("/boot should be detected")
	}
	if !fstabAlreadyHasMount(existing, "/") {
		t.Fatal("/ should be detected")
	}
	// /boots must NOT match /boot — word boundary check.
	if fstabAlreadyHasMount(existing, "/bootloader") {
		t.Fatal("/bootloader should not match /boot")
	}
}

// Barrier pin (ancestor FTA TE-3 MCS-6, reproduced on a real Ubuntu 24.04
// rootfs): a golden image that SHIPS an fstab entry for a mount this
// install owns (e.g. `LABEL=cloudimg-rootfs /`) must have that line
// REPLACED by the node's real UUID entry, or the node mounts / by a
// label that does not exist on its disks and drops to the emergency
// shell. Unrelated image lines survive.
func TestWriteFstabReplacesStaleImageEntry(t *testing.T) {
	sysroot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sysroot, "etc"), 0o750); err != nil {
		t.Fatal(err)
	}
	stale := "LABEL=cloudimg-rootfs\t/\text4\tdiscard,errors=remount-ro\t0 1\n" +
		"tmpfs /dev/shm tmpfs defaults 0 0\n"
	if err := os.WriteFile(filepath.Join(sysroot, "etc/fstab"), []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	sr := result.StorageResult{
		Filesystems: []result.Filesystem{
			{Mount: "/", FS: "xfs", UUID: "1111-2222"},
		},
	}
	if err := WriteFstab(fsops.FS{}, nil, sysroot, sr); err != nil {
		t.Fatalf("WriteFstab: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(sysroot, "etc/fstab")) //nolint:gosec // test file
	got := string(body)
	if strings.Contains(got, "cloudimg-rootfs") {
		t.Fatalf("stale image root entry must be replaced:\n%s", got)
	}
	if !strings.Contains(got, "UUID=1111-2222 / xfs defaults 0 0") {
		t.Fatalf("node root entry missing:\n%s", got)
	}
	if !strings.Contains(got, "/dev/shm") {
		t.Fatalf("unrelated image line must survive:\n%s", got)
	}
	// Idempotency: a second run must not duplicate.
	if err := WriteFstab(fsops.FS{}, nil, sysroot, sr); err != nil {
		t.Fatal(err)
	}
	body2, _ := os.ReadFile(filepath.Join(sysroot, "etc/fstab")) //nolint:gosec // test file
	if strings.Count(string(body2), "UUID=1111-2222") != 1 {
		t.Fatalf("second run duplicated the entry:\n%s", body2)
	}
}
