package bootloader

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fsops"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/result"
)

// stageRHELSysroot creates a fake /sysroot tree with the binaries +
// directories the RHEL adapter checks for. Returns the path.
func stageRHELSysroot(t *testing.T, withEFI bool) string {
	t.Helper()
	sysroot := t.TempDir()
	for _, rel := range []string{
		"usr/sbin/dracut",
		"usr/sbin/grub2-install",
		"usr/sbin/grub2-mkconfig",
		"usr/sbin/efibootmgr",
	} {
		path := filepath.Join(sysroot, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// kernel module dir
	if err := os.MkdirAll(filepath.Join(sysroot, "lib", "modules", "5.14.0-rocky"), 0o750); err != nil {
		t.Fatal(err)
	}
	if withEFI {
		if err := os.MkdirAll(filepath.Join(sysroot, "boot", "efi"), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	return sysroot
}

func TestRHELPrepareHappy(t *testing.T) {
	sysroot := stageRHELSysroot(t, true)
	a := &rhelAdapter{deps: AdapterDeps{
		BootContext: BootContext{
			Sysroot: sysroot, FirmwareMode: "uefi",
			StorageResult: result.StorageResult{BootDevices: []string{"/dev/sda"}},
		},
	}}
	if err := a.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
}

func TestRHELPrepareMissingDracut(t *testing.T) {
	sysroot := t.TempDir() // empty sysroot
	a := &rhelAdapter{deps: AdapterDeps{BootContext: BootContext{Sysroot: sysroot}}}
	err := a.Prepare(context.Background())
	if err == nil || !strings.Contains(err.Error(), "dracut") {
		t.Fatalf("expected dracut error; got %v", err)
	}
}

func TestRHELPrepareNoBootDevices(t *testing.T) {
	sysroot := stageRHELSysroot(t, true)
	a := &rhelAdapter{deps: AdapterDeps{BootContext: BootContext{
		Sysroot: sysroot, FirmwareMode: "uefi",
	}}}
	err := a.Prepare(context.Background())
	if err == nil || !strings.Contains(err.Error(), "boot_devices") {
		t.Fatalf("expected boot_devices error; got %v", err)
	}
}

func TestRHELRegenerateInitramfs(t *testing.T) {
	sysroot := stageRHELSysroot(t, false)
	f := executor.NewFakeRunner(executor.FakeResponse{})
	a := &rhelAdapter{deps: AdapterDeps{
		Runner: f,
		BootContext: BootContext{
			Sysroot: sysroot,
		},
	}}
	if err := a.RegenerateInitramfs(context.Background()); err != nil {
		t.Fatalf("RegenerateInitramfs: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected one call; got %d", len(calls))
	}
	if calls[0].Path != "/usr/bin/env" || calls[0].Chroot != sysroot {
		t.Fatalf("call shape wrong: %+v", calls[0])
	}
	args := strings.Join(calls[0].Args, " ")
	for _, want := range []string{"dracut", "-f", "--omit", "luna", "--kver", "5.14.0-rocky"} {
		if !strings.Contains(args, want) {
			t.Fatalf("dracut args missing %q: %s", want, args)
		}
	}
}

func TestRHELInstallUEFI(t *testing.T) {
	sysroot := stageRHELSysroot(t, true)
	// Pre-create /sys/firmware/efi/efivars so RegisterUEFIEntries
	// would proceed if reached.
	if err := os.MkdirAll(filepath.Join(sysroot, "sys", "firmware", "efi", "efivars"), 0o750); err != nil {
		t.Fatal(err)
	}
	// Pre-create /etc to make writeGrubDefault happy.
	if err := os.MkdirAll(filepath.Join(sysroot, "etc", "default"), 0o750); err != nil {
		t.Fatal(err)
	}
	// Stage the signed shim + grub binaries under /boot/efi/EFI/rocky
	// as if the OS image had been unpacked. Content is sentinel bytes
	// so we can verify the copy faithfully landed in /EFI/BOOT/.
	rockyDir := filepath.Join(sysroot, "boot", "efi", "EFI", "rocky")
	if err := os.MkdirAll(rockyDir, 0o750); err != nil {
		t.Fatal(err)
	}
	shimBody := []byte("SIGNED-SHIM-BLOB")
	grubBody := []byte("SIGNED-GRUB-BLOB-x64")
	if err := os.WriteFile(filepath.Join(rockyDir, "shimx64.efi"), shimBody, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rockyDir, "grubx64.efi"), grubBody, 0o600); err != nil {
		t.Fatal(err)
	}

	f := executor.NewFakeRunner(executor.FakeResponse{})
	// efibootmgr list call: empty NVRAM. We return a synthetic
	// BootOrder so RegisterUEFIEntries proceeds.
	f.AddMatch(executor.FakeMatch{
		Path: "/usr/sbin/efibootmgr",
		Response: executor.FakeResponse{Stdout: []byte(
			"BootOrder: 0010,0020\nBoot0010* UEFI: PXE\nBoot0020* UEFI: Network\n")},
	})

	a := &rhelAdapter{deps: AdapterDeps{
		Runner: f,
		BootContext: BootContext{
			Sysroot: sysroot, FirmwareMode: "uefi", Architecture: "x86_64",
			BootMethod: "grub", BootFirmware: "auto", RegisterNVRAM: "no", // skip the full validate
			KernelArgs: "rd.auto",
			StorageResult: result.StorageResult{
				BootDevices: []string{"/dev/sda", "/dev/sdb"},
				Filesystems: []result.Filesystem{
					{Mount: "/boot", UUID: "53261dca-33d4-4325-8200-4ff99b4a9b5a"},
				},
			},
		},
	}}
	if err := a.Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// /etc/default/grub written.
	body, err := os.ReadFile(filepath.Join(sysroot, "etc", "default", "grub")) //nolint:gosec // test artefact
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"GRUB_TIMEOUT=5",
		"GRUB_DEFAULT=saved",
		"GRUB_DISABLE_SUBMENU=true",
		`GRUB_CMDLINE_LINUX="rd.auto"`,
	} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("/etc/default/grub missing %q:\n%s", want, body)
		}
	}

	// Stub grub.cfg landed in the distro dir, with the /boot UUID.
	stub, err := os.ReadFile(filepath.Join(rockyDir, "grub.cfg")) //nolint:gosec // test artefact
	if err != nil {
		t.Fatalf("stub grub.cfg: %v", err)
	}
	for _, want := range []string{
		"search --no-floppy --fs-uuid --set=dev 53261dca-33d4-4325-8200-4ff99b4a9b5a",
		"set prefix=($dev)/grub2",
		"configfile $prefix/grub.cfg",
	} {
		if !strings.Contains(string(stub), want) {
			t.Fatalf("stub grub.cfg missing %q:\n%s", want, stub)
		}
	}

	// Removable fallback populated with the signed shim (as BOOTX64.EFI)
	// and the signed grubx64.efi alongside it.
	bootDir := filepath.Join(sysroot, "boot", "efi", "EFI", "BOOT")
	if got, _ := os.ReadFile(filepath.Join(bootDir, "BOOTX64.EFI")); string(got) != string(shimBody) { //nolint:gosec // test artefact
		t.Fatalf("BOOTX64.EFI != signed shim:\n  got=%q want=%q", got, shimBody)
	}
	if got, _ := os.ReadFile(filepath.Join(bootDir, "grubx64.efi")); string(got) != string(grubBody) { //nolint:gosec // test artefact
		t.Fatalf("grubx64.efi in /EFI/BOOT != signed grub:\n  got=%q want=%q", got, grubBody)
	}

	// BOOTX64.CSV overridden with our vendor name so shim's fallback.efi
	// auto-creates a "TrinityX" NVRAM entry instead of "rocky". Format
	// is UCS-2 LE without BOM; decode and check it contains the
	// TrinityX vendor token.
	csv, err := os.ReadFile(filepath.Join(rockyDir, "BOOTX64.CSV")) //nolint:gosec // test artefact
	if err != nil {
		t.Fatalf("BOOTX64.CSV: %v", err)
	}
	if len(csv)%2 != 0 {
		t.Fatalf("BOOTX64.CSV byte length is odd, not UCS-2 LE: %d", len(csv))
	}
	// Decode UTF-16 LE → string and assert content.
	u16 := make([]uint16, len(csv)/2)
	for i := 0; i < len(csv); i += 2 {
		u16[i/2] = uint16(csv[i]) | uint16(csv[i+1])<<8
	}
	got := string(utf16.Decode(u16))
	if !strings.HasPrefix(got, "shimx64.efi,TrinityX,,") {
		t.Fatalf("BOOTX64.CSV does not start with shimx64.efi,TrinityX,,...:\n  %q", got)
	}

	// grub2-install --target=x86_64-efi must NOT be called any more.
	gotCmds := envCmds(f)
	for _, g := range gotCmds {
		if strings.Contains(g, "grub2-install") && strings.Contains(g, "--target=") {
			t.Fatalf("unexpected grub2-install UEFI invocation: %s", g)
		}
	}
	// grub2-mkconfig is still required.
	want := "grub2-mkconfig -o /boot/grub2/grub.cfg"
	found := false
	for _, g := range gotCmds {
		if g == want {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected %q in:\n  %s", want, strings.Join(gotCmds, "\n  "))
	}
}

// BIOS/legacy boot is unsupported: Install must refuse a non-UEFI firmware
// mode outright (the installer gate in lpart-storage-prepare already refuses
// BIOS nodes; this is the adapter-level belt) and must NOT run grub2-install.
func TestRHELInstallRefusesBIOS(t *testing.T) {
	sysroot := stageRHELSysroot(t, false)
	if err := os.MkdirAll(filepath.Join(sysroot, "etc", "default"), 0o750); err != nil {
		t.Fatal(err)
	}
	f := executor.NewFakeRunner(executor.FakeResponse{})
	a := &rhelAdapter{deps: AdapterDeps{
		Runner: f,
		BootContext: BootContext{
			Sysroot: sysroot, FirmwareMode: "bios", Architecture: "x86_64",
			KernelArgs:    "rd.auto",
			StorageResult: result.StorageResult{BootDevices: []string{"/dev/sda"}},
		},
	}}
	err := a.Install(context.Background())
	if err == nil {
		t.Fatal("Install must refuse BIOS firmware; got nil error")
	}
	if !strings.Contains(err.Error(), "requires UEFI") {
		t.Fatalf("error should name the UEFI requirement; got: %v", err)
	}
	for _, g := range envCmds(f) {
		if strings.Contains(g, "grub2-install") {
			t.Fatalf("BIOS refusal must not run grub2-install; saw: %s", g)
		}
	}
}

func TestRHELValidatePicksTrinityOrFallback(t *testing.T) {
	sysroot := stageRHELSysroot(t, true)
	if err := os.MkdirAll(filepath.Join(sysroot, "boot", "grub2"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sysroot, "boot", "grub2", "grub.cfg"), []byte("set"), 0o644); err != nil { //nolint:gosec // test artefact
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sysroot, "etc"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sysroot, "etc", "fstab"), []byte("UUID=x / xfs defaults 0 0\n"), 0o644); err != nil { //nolint:gosec // test artefact
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sysroot, "boot", "efi", "EFI", "TrinityX"), 0o750); err != nil {
		t.Fatal(err)
	}
	a := &rhelAdapter{deps: AdapterDeps{BootContext: BootContext{Sysroot: sysroot, FirmwareMode: "uefi"}}}
	if err := a.Validate(context.Background()); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestRHELValidateMissingGrubCfg(t *testing.T) {
	sysroot := stageRHELSysroot(t, true)
	a := &rhelAdapter{deps: AdapterDeps{BootContext: BootContext{Sysroot: sysroot, FirmwareMode: "uefi"}}}
	err := a.Validate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "grub.cfg") {
		t.Fatalf("expected grub.cfg error; got %v", err)
	}
}

// envCmds extracts the wrapped command names from `env <cmd> <args>`
// calls so the test can assert on the cmd shape, not the env prefix.
func envCmds(f *executor.FakeRunner) []string {
	out := []string{}
	for _, c := range f.Calls() {
		if c.Path != "/usr/bin/env" {
			continue
		}
		out = append(out, strings.Join(c.Args, " "))
	}
	return out
}

// writeBLSEntry stages a /boot/loader/entries/<name> file under sysroot.
func writeBLSEntry(t *testing.T, sysroot, name, body string) string {
	t.Helper()
	dir := filepath.Join(sysroot, "boot", "loader", "entries")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The corrupt el9_8 entry observed on node001 2026-06-04: kernel-install
// ran inside the image chroot at /trinity/images/compute-image, so the
// newest kernel's BLS entry got the build-path prefix on linux/initrd and
// a build-time root=/dev/mapper/vg_rhel-lv_root in options. GRUB defaults
// to the newest kernel -> "vmlinuz ... not found" -> rescue.
const corrupt687Entry = `title Rocky Linux (5.14.0-687.12.1.el9_8.x86_64) 9.7 (Blue Onyx)
version 5.14.0-687.12.1.el9_8.x86_64
linux /trinity/images/compute-image/boot/vmlinuz-5.14.0-687.12.1.el9_8.x86_64
initrd /trinity/images/compute-image/boot/initramfs-5.14.0-687.12.1.el9_8.x86_64.img $tuned_initrd
options root=/dev/mapper/vg_rhel-lv_root ro crashkernel=1G-2G:192M resume=/dev/mapper/vg_rhel-swap rd.lvm.lv=vg_rhel/lv_root rd.lvm.lv=vg_rhel/swap $tuned_params
grub_users $grub_users
grub_arg --unrestricted
grub_class rocky
`

// A clean entry (older kernel) — linux/initrd already /boot-relative,
// options carries no device-specific tokens. Normalisation must leave its
// semantics intact (only the path prefix shifts with the boot layout).
const clean611Entry = `title Rocky Linux (5.14.0-611.30.1.el9_7.x86_64) 9.7 (Blue Onyx)
version 5.14.0-611.30.1.el9_7.x86_64
linux /boot/vmlinuz-5.14.0-611.30.1.el9_7.x86_64
initrd /boot/initramfs-5.14.0-611.30.1.el9_7.x86_64.img $tuned_initrd
options  $tuned_params
grub_users $grub_users
grub_class rocky
`

func TestNormalizeBLSEntriesSeparateBoot(t *testing.T) {
	sysroot := stageRHELSysroot(t, true)
	bad := writeBLSEntry(t, sysroot, "id-5.14.0-687.12.1.el9_8.x86_64.conf", corrupt687Entry)
	good := writeBLSEntry(t, sysroot, "id-5.14.0-611.30.1.el9_7.x86_64.conf", clean611Entry)

	a := &rhelAdapter{deps: AdapterDeps{BootContext: BootContext{
		Sysroot: sysroot,
		// /boot is its own filesystem -> paths must be relative to it.
		StorageResult: result.StorageResult{Filesystems: []result.Filesystem{{Mount: "/boot"}}},
	}}}
	a.normalizeBLSEntries()

	gotBad := readFile(t, bad)
	for _, want := range []string{
		"linux /vmlinuz-5.14.0-687.12.1.el9_8.x86_64",
		"initrd /initramfs-5.14.0-687.12.1.el9_8.x86_64.img $tuned_initrd",
	} {
		if !strings.Contains(gotBad, want) {
			t.Errorf("separate-boot 687 entry missing %q\n--- got ---\n%s", want, gotBad)
		}
	}
	// build-path prefix must be gone entirely
	if strings.Contains(gotBad, "/trinity/images/compute-image") {
		t.Errorf("687 entry still carries the build path:\n%s", gotBad)
	}
	// build-time root / rd.lvm.lv / resume tokens must be stripped
	for _, badTok := range []string{"root=/dev/mapper/vg_rhel-lv_root", "rd.lvm.lv=vg_rhel/lv_root", "rd.lvm.lv=vg_rhel/swap", "resume=/dev/mapper/vg_rhel-swap"} {
		if strings.Contains(gotBad, badTok) {
			t.Errorf("687 entry still carries build token %q:\n%s", badTok, gotBad)
		}
	}
	// harmless generic tokens are preserved
	for _, keepTok := range []string{"options ", "ro", "crashkernel=1G-2G:192M", "$tuned_params"} {
		if !strings.Contains(gotBad, keepTok) {
			t.Errorf("687 entry dropped a generic token %q:\n%s", keepTok, gotBad)
		}
	}

	// clean entry: path shifts to /-relative, options semantics unchanged
	gotGood := readFile(t, good)
	if !strings.Contains(gotGood, "linux /vmlinuz-5.14.0-611.30.1.el9_7.x86_64") {
		t.Errorf("clean 611 entry not made boot-relative:\n%s", gotGood)
	}
	if !strings.Contains(gotGood, "options $tuned_params") {
		t.Errorf("clean 611 options changed unexpectedly:\n%s", gotGood)
	}
}

func TestNormalizeBLSEntriesSharedBoot(t *testing.T) {
	sysroot := stageRHELSysroot(t, true)
	bad := writeBLSEntry(t, sysroot, "id-5.14.0-687.12.1.el9_8.x86_64.conf", corrupt687Entry)

	// No /boot filesystem in storage-result -> /boot is a dir on root ->
	// paths must keep the /boot/ prefix.
	a := &rhelAdapter{deps: AdapterDeps{BootContext: BootContext{
		Sysroot:       sysroot,
		StorageResult: result.StorageResult{Filesystems: []result.Filesystem{{Mount: "/"}}},
	}}}
	a.normalizeBLSEntries()

	got := readFile(t, bad)
	if !strings.Contains(got, "linux /boot/vmlinuz-5.14.0-687.12.1.el9_8.x86_64") {
		t.Errorf("shared-boot 687 linux path wrong:\n%s", got)
	}
	if !strings.Contains(got, "initrd /boot/initramfs-5.14.0-687.12.1.el9_8.x86_64.img $tuned_initrd") {
		t.Errorf("shared-boot 687 initrd path wrong:\n%s", got)
	}
	if strings.Contains(got, "/trinity/images/compute-image") {
		t.Errorf("shared-boot 687 still carries build path:\n%s", got)
	}
}

// A path with no /boot/ segment (defensive) is left untouched.
func TestNormalizeBootPathNoMarker(t *testing.T) {
	if got := normalizeBootPath("/vmlinuz-x", "/"); got != "/vmlinuz-x" {
		t.Errorf("expected unchanged, got %q", got)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p) //nolint:gosec // test artefact
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The grub2-mkconfig output on a metadata-1.0 RAID locates /boot + root via
// the md-array UUID, which GRUB resolves non-deterministically when a mirror
// leg is missing. rewriteMduuidToFSUUID must drop the mduuid set/hints so the
// fs-uuid scan (which finds a raw 1.0 member) is used.
func TestRewriteMduuidToFSUUID(t *testing.T) {
	in := `menuentry 'Rocky' {
	insmod part_gpt
	insmod xfs
	set root='mduuid/3dbc7f6c203c674664990e4cdd81136b'
	if [ x$feature_platform_search_hint = xy ]; then
	  search --no-floppy --fs-uuid --set=root --hint='mduuid/3dbc7f6c203c674664990e4cdd81136b'  4e345124-e054-47a8-8919-f040540db660
	else
	  search --no-floppy --fs-uuid --set=root 4e345124-e054-47a8-8919-f040540db660
	fi
	set boot='mduuid/32e428e209fcc9332d6f0cb762da7821'
	linux /vmlinuz-x root=/dev/mapper/os-root
}
`
	out := rewriteMduuidToFSUUID(in)
	if strings.Contains(out, "mduuid/") {
		t.Errorf("mduuid references survived:\n%s", out)
	}
	// the fs-uuid search must remain, now hint-free
	if !strings.Contains(out, "search --no-floppy --fs-uuid --set=root  4e345124-e054-47a8-8919-f040540db660") &&
		!strings.Contains(out, "search --no-floppy --fs-uuid --set=root 4e345124-e054-47a8-8919-f040540db660") {
		t.Errorf("fs-uuid search line missing/garbled:\n%s", out)
	}
	// kernel line untouched
	if !strings.Contains(out, "linux /vmlinuz-x root=/dev/mapper/os-root") {
		t.Errorf("kernel line changed:\n%s", out)
	}
	// the standalone set root='mduuid/..' line is gone
	if strings.Contains(out, "set root='mduuid") || strings.Contains(out, "set boot='mduuid") {
		t.Errorf("set mduuid line survived:\n%s", out)
	}
}

// No mduuid (non-RAID) -> unchanged.
func TestRewriteMduuidNoop(t *testing.T) {
	in := "menuentry x {\n\tsearch --no-floppy --fs-uuid --set=root ABCD-1234\n}\n"
	if out := rewriteMduuidToFSUUID(in); out != in {
		t.Errorf("non-RAID grub.cfg changed: %q", out)
	}
}

// --dry-run must not write the target: /etc/default/grub, the BLS entries and
// the degraded-safe grub.cfg rewrite all go through the dry-run-aware fsops.FS.
func TestDryRunWritesNothing(t *testing.T) {
	sysroot := stageRHELSysroot(t, true)
	grubDefault := filepath.Join(sysroot, "etc", "default", "grub")
	if err := os.MkdirAll(filepath.Dir(grubDefault), 0o750); err != nil {
		t.Fatal(err)
	}
	const imageDefault = "GRUB_TIMEOUT=1\nGRUB_CMDLINE_LINUX=\"console=ttyS0\"\n"
	if err := os.WriteFile(grubDefault, []byte(imageDefault), 0o600); err != nil {
		t.Fatal(err)
	}
	bls := writeBLSEntry(t, sysroot, "id-5.14.0-687.12.1.el9_8.x86_64.conf", corrupt687Entry)
	grubCfg := filepath.Join(sysroot, "boot", "grub2", "grub.cfg")
	if err := os.MkdirAll(filepath.Dir(grubCfg), 0o750); err != nil {
		t.Fatal(err)
	}
	const mdCfg = "set root='mduuid/3dbc7f6c203c674664990e4cdd81136b'\nlinux /vmlinuz-x\n"
	if err := os.WriteFile(grubCfg, []byte(mdCfg), 0o600); err != nil {
		t.Fatal(err)
	}

	a := &rhelAdapter{deps: AdapterDeps{
		FS: fsops.FS{DryRun: true},
		BootContext: BootContext{
			Sysroot: sysroot, KernelArgs: "rd.auto",
			StorageResult: result.StorageResult{Filesystems: []result.Filesystem{{Mount: "/boot"}}},
		},
	}}
	if err := a.writeGrubDefault(); err != nil {
		t.Fatalf("writeGrubDefault: %v", err)
	}
	a.normalizeBLSEntries()
	a.makeGrubCfgDegradedSafe()

	if got := readFile(t, grubDefault); got != imageDefault {
		t.Errorf("dry-run rewrote /etc/default/grub:\n%s", got)
	}
	if got := readFile(t, bls); got != corrupt687Entry {
		t.Errorf("dry-run rewrote the BLS entry:\n%s", got)
	}
	if got := readFile(t, grubCfg); got != mdCfg {
		t.Errorf("dry-run rewrote grub.cfg:\n%s", got)
	}
}
