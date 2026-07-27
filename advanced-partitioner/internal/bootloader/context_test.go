package bootloader

import (
	"os"
	"path/filepath"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/result"
)

func writeOSRelease(t *testing.T, dir, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "etc"), 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "etc", "os-release")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDetectFamilyRHEL(t *testing.T) {
	tmp := t.TempDir()
	for _, body := range []string{
		`ID="rocky"` + "\n",
		`ID=rhel` + "\n",
		`ID="alma"` + "\n",
		`ID="centos"` + "\n" + `ID_LIKE="rhel fedora"` + "\n",
		`ID="fedora"` + "\n",
	} {
		path := writeOSRelease(t, tmp, body)
		got, err := DetectFamily(path)
		if err != nil {
			t.Fatalf("body=%q: %v", body, err)
		}
		if got != FamilyRHEL {
			t.Fatalf("body=%q: got %q, want %q", body, got, FamilyRHEL)
		}
	}
}

func TestDetectFamilyDebian(t *testing.T) {
	tmp := t.TempDir()
	for _, body := range []string{
		`ID=ubuntu` + "\n",
		`ID="debian"` + "\n",
		`ID=ubuntu` + "\n" + `ID_LIKE=debian` + "\n",
	} {
		path := writeOSRelease(t, tmp, body)
		got, err := DetectFamily(path)
		if err != nil {
			t.Fatalf("body=%q: %v", body, err)
		}
		if got != FamilyDebian {
			t.Fatalf("body=%q: got %q, want %q", body, got, FamilyDebian)
		}
	}
}

func TestDetectFamilySUSE(t *testing.T) {
	tmp := t.TempDir()
	for _, body := range []string{
		`ID="sles"` + "\n",
		`ID="opensuse-leap"` + "\n",
		`ID="suse"` + "\n",
	} {
		path := writeOSRelease(t, tmp, body)
		got, err := DetectFamily(path)
		if err != nil {
			t.Fatalf("body=%q: %v", body, err)
		}
		if got != FamilySUSE {
			t.Fatalf("body=%q: got %q", body, got)
		}
	}
}

func TestDetectFamilyUnknown(t *testing.T) {
	tmp := t.TempDir()
	path := writeOSRelease(t, tmp, `ID="archlinux"`+"\n"+`ID_LIKE=""`+"\n")
	if _, err := DetectFamily(path); err == nil {
		t.Fatal("expected unsupported-family error")
	}
}

func TestDetectFamilyMissingFile(t *testing.T) {
	if _, err := DetectFamily("/no/such/file"); err == nil {
		t.Fatal("expected missing-file error")
	}
}

func TestArchToUEFITarget(t *testing.T) {
	cases := map[string]string{
		"x86_64":  "x86_64-efi",
		"amd64":   "x86_64-efi",
		"aarch64": "arm64-efi",
		"arm64":   "arm64-efi",
	}
	for arch, want := range cases {
		got, err := ArchToUEFITarget(arch)
		if err != nil {
			t.Fatalf("arch=%s: %v", arch, err)
		}
		if got != want {
			t.Fatalf("arch=%s: got %s want %s", arch, got, want)
		}
	}
	if _, err := ArchToUEFITarget("riscv64"); err == nil {
		t.Fatal("expected unsupported arch error")
	}
}

func TestArchToUEFIFallbackLoader(t *testing.T) {
	cases := map[string]string{
		"x86_64":  `\EFI\BOOT\BOOTX64.EFI`,
		"aarch64": `\EFI\BOOT\BOOTAA64.EFI`,
	}
	for arch, want := range cases {
		got, err := ArchToUEFIFallbackLoader(arch)
		if err != nil {
			t.Fatalf("arch=%s: %v", arch, err)
		}
		if got != want {
			t.Fatalf("arch=%s: got %s want %s", arch, got, want)
		}
	}
}

func TestKernelArgsRDMDAndLVM(t *testing.T) {
	sr := result.StorageResult{
		StorageType: "lvm",
		RAIDMode:    "raid1",
		Filesystems: []result.Filesystem{
			{Mount: "/", FS: "xfs", Device: "/dev/mapper/os-root"},
		},
	}
	got := KernelArgs(sr)
	want := "rd.auto rd.md=1 rd.lvm=1 rootfstype=xfs"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestKernelArgsTmpfsNoExtraFlags(t *testing.T) {
	sr := result.StorageResult{
		StorageType: "memory",
		RAIDMode:    "none",
		Filesystems: []result.Filesystem{
			{Mount: "/", FS: "tmpfs", Device: "tmpfs"},
		},
	}
	got := KernelArgs(sr)
	if got != "rd.auto" {
		t.Fatalf("got %q, want rd.auto", got)
	}
}

func TestGrubCmdlineValue(t *testing.T) {
	got := GrubCmdlineValue("rd.auto rd.md=1")
	want := `GRUB_CMDLINE_LINUX="rd.auto rd.md=1"`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildBootContextMemoryRoot(t *testing.T) {
	sr := result.StorageResult{
		MountedTargetRoot: "/sysroot",
		BootMethod:        "none",
		FirmwareMode:      "memory",
		StorageType:       "memory",
		RAIDMode:          "none",
		Filesystems: []result.Filesystem{
			{Mount: "/", FS: "tmpfs", Device: "tmpfs"},
		},
	}
	ctx, err := BuildBootContext(sr, "x86_64", "/no/such/os-release-needed")
	if err != nil {
		t.Fatalf("BuildBootContext: %v", err)
	}
	if ctx.Family != FamilyNone {
		t.Fatalf("Family = %s, want %s", ctx.Family, FamilyNone)
	}
	if ctx.KernelArgs != "rd.auto" {
		t.Fatalf("KernelArgs = %q", ctx.KernelArgs)
	}
}

func TestBuildBootContextRHELDisk(t *testing.T) {
	dir := t.TempDir()
	writeOSRelease(t, dir, `ID="rocky"`+"\n")
	sr := result.StorageResult{
		MountedTargetRoot: dir,
		BootMethod:        "grub",
		BootFirmware:      "auto",
		FirmwareMode:      "uefi",
		StorageType:       "lvm",
		RAIDMode:          "raid1",
		Filesystems: []result.Filesystem{
			{Mount: "/", FS: "xfs", Device: "/dev/mapper/os-root"},
		},
	}
	ctx, err := BuildBootContext(sr, "x86_64", "")
	if err != nil {
		t.Fatalf("BuildBootContext: %v", err)
	}
	if ctx.Family != FamilyRHEL {
		t.Fatalf("Family = %s, want %s", ctx.Family, FamilyRHEL)
	}
	if ctx.Sysroot != dir {
		t.Fatalf("Sysroot = %q", ctx.Sysroot)
	}
	if !contains(ctx.KernelArgs, "rd.md=1") || !contains(ctx.KernelArgs, "rd.lvm=1") {
		t.Fatalf("KernelArgs missing rd.md/rd.lvm: %q", ctx.KernelArgs)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(sub) > 0 && indexOf(s, sub) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
