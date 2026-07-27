package bootloader

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fsops"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/result"
)

// UEFI install for every GRUB-using distro (RHEL, Rocky, Alma,
// CentOS, Fedora, Debian, Ubuntu, and — when SUSE support lands —
// SLES, openSUSE) uses the distro's pre-built signed shim + grubx64.efi
// binaries shipped in the shim-* and grub2-efi-* / grub-efi-amd64-signed
// RPMs/debs and unpacked by lpart-osimage-install into
// /sysroot/boot/efi/EFI/<distro>/.
//
// This sidesteps a real bug in `grub2-install --target=x86_64-efi`
// (and the identically-broken Debian `grub-install
// --target=x86_64-efi`): both rebuild a custom grub EFI image whose
// autodetected module set is just enough to reach /boot — typically
// `fshelp xfs part_gpt diskfilter mdraid1x` (5 modules, ~250 KB) —
// and is missing the runtime-essential `normal` (the script
// interpreter — without it grub.cfg cannot be parsed at all), plus
// `linux`, `initrd`, `configfile`, `search`*, `blscfg`, `lvm`, `fat`,
// and the display modules. The result is that GRUB starts, drops to
// the rescue prompt because it cannot even parse grub.cfg, and the
// machine never reaches the kernel.
//
// The signed grubx64.efi has a fixed prefix `/EFI/<distro>` (e.g.
// `/EFI/rocky` / `/EFI/debian`) baked in at package build time, so
// it always loads `<ESP>/EFI/<distro>/grub.cfg`. The RPM/deb ships
// the binary but does NOT ship that grub.cfg — Anaconda / debootstrap
// writes it during install. Our finalise step writes the stub here so
// the signed binary can find the real grub.cfg under
// /boot/<grubDirName>/.
//
// Removable fallback (`<ESP>/EFI/BOOT/BOOTX64.EFI` on amd64;
// `BOOTAA64.EFI` on arm64) gets a copy of the shim and a sibling
// copy of the signed grub binary, so the same boot chain works
// when NVRAM has been cleared and the firmware falls back to the
// removable-media path. The stub grub.cfg is read by both copies
// because the signed grub's prefix is absolute.
//
// BOOTX64.CSV is overwritten with a TrinityX-labelled body. NOTE
// (shim 15.8, RHEL/Rocky 9): empirically, fbx64.efi ignores the
// CSV's label field and uses its compiled-in vendor name as the
// EFI_LOAD_OPTION Description, so the auto-created fallback entry
// still shows up as e.g. "Boot####* rocky" regardless of what we
// write. Verified by dumping the raw `/sys/firmware/efi/efivars/
// Boot####-*` bytes: the Description was the hard-coded vendor
// string, not our CSV label. The CSV write is kept anyway because
// (a) future shim versions may honour the label field, and (b) it
// overrides any stale distro-shipped CSV so we are not relying on
// the OS-image's text. The cosmetic mislabel is harmless: the
// primary `Boot####* TrinityX <disk>` entries (registered by
// RegisterUEFIEntries in nvram.go) are first in BootOrder and are
// what firmware actually selects; the "rocky"/"debian"/... entry
// only matters if shim's fallback is ever invoked.

// StageSignedShimOpts is the input to StageSignedShim.
type StageSignedShimOpts struct {
	// FS is the dry-run-aware native fs chokepoint (zero value = real).
	FS fsops.FS
	// Sysroot is the path the installed OS is mounted at (typically
	// "/sysroot"). The ESP is expected at <sysroot>/boot/efi.
	Sysroot string
	// Arch is the target architecture in uname form ("x86_64" or
	// "aarch64"). Drives shim/grub binary names and the removable
	// fallback loader path.
	Arch string
	// StorageResult is the just-prepared storage layout. Used to look
	// up the /boot filesystem UUID — required because the stub
	// grub.cfg uses search --fs-uuid.
	StorageResult result.StorageResult
	// GrubDirName is the path component under /boot containing the
	// real grub.cfg. "grub2" for RHEL family; "grub" for Debian.
	// The stub config issues `configfile ($dev)/<GrubDirName>/grub.cfg`.
	GrubDirName string
	// Family selects the signed-pair source-table row used to populate
	// an empty ESP (see signedPairSources). Families whose images ship
	// a pre-populated EFI/<distro>/ (RHEL) have an empty row.
	Family Family
	// Logger, when non-nil, receives a user-visible line when a signed
	// pair is staged from the image into the ESP.
	Logger *txlog.Logger
}

// signedPairSource lists where a family's OS image keeps the Secure Boot
// signed shim+grub binaries when the image does NOT pre-populate the ESP's
// EFI/<distro>/ directory. Paths are sysroot-relative; the first existing
// candidate wins. This is the ONLY per-family variation in the signed-shim
// flow — adding a family to the UEFI install = adding a table row in
// signedPairSources.
type signedPairSource struct {
	shim []string // candidate paths, sysroot-relative
	grub []string
	hint string // package names for the "not found" error message
}

// signedPairSources returns the family's source-table row for arch. An
// empty row (nil shim) means the family's image tarball ships a populated
// EFI/<distro>/ in the ESP and no pre-staging is needed (RHEL family).
func signedPairSources(family Family, arch string) (signedPairSource, error) {
	shimName, grubName, err := archShimAndGrub(arch)
	if err != nil {
		return signedPairSource{}, err
	}
	switch family {
	case FamilyDebian:
		// shim-signed + grub-efi-<arch>-signed packages.
		d := debianSignedGrubDir(arch)
		return signedPairSource{
			shim: []string{
				"usr/lib/shim/" + shimName + ".signed",
				"usr/lib/shim/" + shimName + ".signed.latest",
				"usr/lib/shim/" + shimName,
			},
			grub: []string{
				"usr/lib/grub/" + d + "/" + grubName + ".signed",
				"usr/lib/grub/" + d + "/" + grubName,
			},
			hint: "shim-signed + grub-efi-" + arch + "-signed",
		}, nil
	case FamilySUSE:
		// shim + grub2-x86_64-efi packages (openSUSE Leap / SLE).
		return signedPairSource{
			shim: []string{
				"usr/lib64/efi/shim.efi",
				"usr/lib64/efi/shim-sles.efi",
			},
			grub: []string{
				"usr/lib64/efi/grub.efi",
				"usr/share/grub2/x86_64-efi/grub.efi",
				"usr/share/efi/x86_64/grub.efi",
			},
			hint: "shim + grub2-x86_64-efi",
		}, nil
	default:
		// FamilyRHEL: the image ships EFI/<distro>/ populated. FamilyNone
		// never reaches the UEFI flow.
		return signedPairSource{}, nil
	}
}

// ensureSignedPairStaged populates <ESP>/EFI/<distro>/{shim,grub}<arch>.efi
// from the family's source-table row when the ESP does not already hold a
// signed pair. No-op when already staged (a pre-populated image, or an
// earlier run) or when the family has no row (RHEL). One body for every
// family — the per-family difference is data (signedPairSources), not code.
func ensureSignedPairStaged(opts StageSignedShimOpts) error {
	espRoot := filepath.Join(opts.Sysroot, "boot", "efi")
	if _, _, _, err := findSignedShimDir(espRoot, opts.Arch); err == nil {
		return nil // already staged
	}
	src, err := signedPairSources(opts.Family, opts.Arch)
	if err != nil {
		return err
	}
	if len(src.shim) == 0 {
		// Family expects a pre-populated ESP; StageSignedShim's own
		// findSignedShimDir will report the miss with a clear error.
		return nil
	}
	shimName, grubName, err := archShimAndGrub(opts.Arch)
	if err != nil {
		return err
	}
	distro := espDistroDirName(opts.Sysroot)
	destDir := filepath.Join(espRoot, "EFI", distro)
	if err := opts.FS.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", destDir, err)
	}
	shimSrc := firstExistingAbs(prefixSysroot(opts.Sysroot, src.shim)...)
	grubSrc := firstExistingAbs(prefixSysroot(opts.Sysroot, src.grub)...)
	if shimSrc == "" || grubSrc == "" {
		return fmt.Errorf(
			"%s signed shim/grub not found in image (shim=%q grub=%q); need the %s packages",
			opts.Family, shimSrc, grubSrc, src.hint)
	}
	if err := copyEFIFile(opts.FS, shimSrc, filepath.Join(destDir, shimName)); err != nil {
		return fmt.Errorf("stage signed shim: %w", err)
	}
	if err := copyEFIFile(opts.FS, grubSrc, filepath.Join(destDir, grubName)); err != nil {
		return fmt.Errorf("stage signed grub: %w", err)
	}
	// Shim's chained-loader name is baked in per DISTRO BUILD: RH/Debian
	// shims load grub<arch>.efi, SUSE builds load plain grub.efi (proven
	// live: the surviving raid1 leg's boot died "Failed to open
	// \EFI\BOOT\grub.efi" and fell through to PXE re-provision, suse 3.28).
	// Stage the grub under BOTH names so every family's shim finds its
	// expected companion — a ~2MB duplicate instead of a per-family branch.
	if grubName != "grub.efi" {
		if err := copyEFIFile(opts.FS, grubSrc, filepath.Join(destDir, "grub.efi")); err != nil {
			return fmt.Errorf("stage signed grub (grub.efi alias): %w", err)
		}
	}
	if opts.Logger != nil {
		opts.Logger.UserInfo("staged %s signed shim+grub into EFI/%s/", opts.Family, distro)
	}
	return nil
}

// prefixSysroot joins each sysroot-relative path onto sysroot.
func prefixSysroot(sysroot string, rels []string) []string {
	abs := make([]string, len(rels))
	for i, r := range rels {
		abs[i] = filepath.Join(sysroot, r)
	}
	return abs
}

// espDistroDirName returns the os-release ID of the image ("ubuntu",
// "opensuse-leap", "sles", ...), used as the ESP EFI/<id>/ directory name
// when staging the signed pair. The name is cosmetic — StageSignedShim
// scans every EFI/* directory — so an unreadable os-release falls back to
// a neutral "linux".
func espDistroDirName(sysroot string) string {
	body, err := os.ReadFile(filepath.Join(sysroot, "etc", "os-release")) //nolint:gosec // sysroot file
	if err == nil {
		for _, ln := range strings.Split(string(body), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(ln), "ID="); ok {
				id := strings.Trim(strings.TrimSpace(v), `"`)
				if id != "" {
					return id
				}
			}
		}
	}
	return "linux"
}

// debianSignedGrubDir maps the arch to the grub-efi-*-signed package's
// directory under /usr/lib/grub.
func debianSignedGrubDir(arch string) string {
	switch arch {
	case "aarch64", "arm64":
		return "arm64-efi-signed"
	default:
		return "x86_64-efi-signed"
	}
}

// firstExistingAbs returns the first path that exists, or "".
func firstExistingAbs(paths ...string) string {
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// StageSignedShim implements the canonical signed-shim UEFI install
// shared by every GRUB-using family. It:
//
//  1. stages the signed pair into <espRoot>/EFI/<distro>/ from the
//     family's package locations when the ESP is empty (data-driven,
//     see signedPairSources; no-op for pre-populated RHEL images)
//  2. locates <espRoot>/EFI/<distro>/{shim,grub} from the OS image
//  3. writes a stub <distro>/grub.cfg that search-by-fs-uuid hands off
//     to /boot/<GrubDirName>/grub.cfg
//  4. overwrites <distro>/BOOT<arch>.CSV with a TrinityX-labelled
//     body (e.g. BOOTX64.CSV on amd64, BOOTAA64.CSV on arm64)
//  5. copies the shim to <espRoot>/EFI/BOOT/<fallbackLoader> and the
//     grub binary alongside it for the removable-media fallback
//
// Caller still needs to generate /boot/<GrubDirName>/grub.cfg
// (grub2-mkconfig / update-grub) and register NVRAM via efibootmgr.
func StageSignedShim(opts StageSignedShimOpts) error {
	espRoot := filepath.Join(opts.Sysroot, "boot", "efi")

	if err := ensureSignedPairStaged(opts); err != nil {
		return fmt.Errorf("StageSignedShim: %w", err)
	}

	distroDir, shimPath, grubPath, err := findSignedShimDir(espRoot, opts.Arch)
	if err != nil {
		return fmt.Errorf("StageSignedShim: %w", err)
	}

	bootUUID := pickBootFSUUID(opts.StorageResult)
	if bootUUID == "" {
		return fmt.Errorf("StageSignedShim: no /boot filesystem UUID in storage-result (signed-shim path requires a separate /boot mount)")
	}

	stubPath := filepath.Join(distroDir, "grub.cfg")
	if err := writeStubGrubCfg(opts.FS, stubPath, bootUUID, opts.GrubDirName); err != nil {
		return fmt.Errorf("write stub %s: %w", stubPath, err)
	}

	fallbackName, err := fallbackLoaderBaseName(opts.Arch)
	if err != nil {
		return err
	}
	// Override the distro-shipped CSV so shim's fallback.efi
	// registers the auto-created NVRAM entry under our label (on
	// shim versions that honour the CSV's vendor field — shim 15.8
	// does not, but writing the CSV is still safe and forward-
	// compatible; see the file-top note). The CSV name mirrors the
	// per-arch fallback loader (BOOTX64.CSV on amd64,
	// BOOTAA64.CSV on arm64).
	shimBase := filepath.Base(shimPath)
	csvName := strings.TrimSuffix(fallbackName, ".EFI") + ".CSV"
	csvPath := filepath.Join(distroDir, csvName)
	if err := writeShimCSV(opts.FS, csvPath, shimBase, BootloaderID, "TrinityX-managed boot entry"); err != nil {
		return fmt.Errorf("write %s: %w", csvPath, err)
	}

	if err := installRemovableFallback(opts.FS, espRoot, shimPath, grubPath, fallbackName); err != nil {
		return err
	}
	// SUSE signed grub derives $prefix from WHERE IT WAS LOADED (their
	// cmdpath patch): booted via EFI/BOOT it reads EFI/BOOT/grub.cfg, not
	// the baked /EFI/<distro> RH/Debian grubs use. Without a stub there,
	// every boot through the removable path lands at a bare `grub>` (proven
	// live at the prompt: prefix='(hd2,gpt1)/EFI/BOOT', and typing
	// `configfile /EFI/sle/grub.cfg` booted the degraded node -- suse 3.28).
	// Write the same stub beside the fallback loader; RH/Debian grubs never
	// read it (baked absolute prefix), so this is inert everywhere else.
	bootStub := filepath.Join(espRoot, "EFI", "BOOT", "grub.cfg")
	if err := writeStubGrubCfg(opts.FS, bootStub, bootUUID, opts.GrubDirName); err != nil {
		return fmt.Errorf("write stub %s: %w", bootStub, err)
	}
	return nil
}

// archShimAndGrub returns the per-arch filenames of the signed shim
// and the signed grub binaries inside <ESP>/EFI/<distro>/. The names
// are stable across rocky/rhel/centos/alma/fedora/debian/ubuntu —
// they come from the shim-* and grub2-efi-* / grub-efi-amd64-signed
// packages.
func archShimAndGrub(arch string) (shim string, grub string, err error) {
	switch arch {
	case "x86_64", "amd64":
		return "shimx64.efi", "grubx64.efi", nil
	case "aarch64", "arm64":
		return "shimaa64.efi", "grubaa64.efi", nil
	default:
		return "", "", fmt.Errorf("unsupported UEFI shim architecture: %s", arch)
	}
}

// findSignedShimDir returns the first directory under
// <espRoot>/EFI/* that contains both the per-arch shim and grub
// binaries. The /EFI/BOOT/ removable-media directory is skipped
// because it does not represent the distro-owned signed pair.
func findSignedShimDir(espRoot, arch string) (distroDir, shimPath, grubPath string, err error) {
	shimName, grubName, err := archShimAndGrub(arch)
	if err != nil {
		return "", "", "", err
	}
	efiRoot := filepath.Join(espRoot, "EFI")
	entries, err := os.ReadDir(efiRoot)
	if err != nil {
		return "", "", "", fmt.Errorf("read %s: %w", efiRoot, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.EqualFold(e.Name(), "BOOT") {
			continue
		}
		cand := filepath.Join(efiRoot, e.Name())
		s := filepath.Join(cand, shimName)
		g := filepath.Join(cand, grubName)
		if _, err := os.Stat(s); err != nil {
			continue
		}
		if _, err := os.Stat(g); err != nil {
			continue
		}
		return cand, s, g, nil
	}
	return "", "", "", fmt.Errorf(
		"no signed shim+grub found under %s (need <distro>/{%s,%s} from the shim-* and grub2-efi-* / grub-efi-amd64-signed packages in the OS image)",
		efiRoot, shimName, grubName)
}

// writeStubGrubCfg renders the stub grub.cfg at path. bootFSUUID
// must be the UUID of the /boot filesystem (as `blkid -s UUID -o
// value` reports it). grubDirName is the path component under /boot
// where the real grub.cfg lives ("grub2" for RHEL family, "grub" for
// Debian).
func writeStubGrubCfg(fs fsops.FS, path, bootFSUUID, grubDirName string) error {
	body := fmt.Sprintf(
		"search --no-floppy --fs-uuid --set=dev %s\n"+
			"set prefix=($dev)/%s\n"+
			"export prefix\n"+
			"configfile $prefix/grub.cfg\n",
		bootFSUUID, grubDirName,
	)
	return fs.WriteFile(path, []byte(body), 0o644)
}

// writeShimCSV writes a UEFI fallback CSV at path. The format
// (UCS-2 LE, no BOM, comma-separated, LF line terminator):
//
//	<shimName>,<vendor>,,<description>\n
//
// Shim's fallback.efi reads CSV files in every /EFI/* directory and
// auto-creates an NVRAM Boot#### entry whose loader path is
// `\EFI\<csvDir>\<shimName>`. The "vendor" field is intended to
// become the EFI_LOAD_OPTION Description, but shim 15.8 (RHEL/Rocky
// 9) ignores it and uses its own compiled-in vendor name — see the
// note at the top of this file. We still write the CSV so that
// future shim versions (or other distros' shim builds) that honour
// the label field will show "TrinityX".
func writeShimCSV(fs fsops.FS, path, shimName, vendor, description string) error {
	text := shimName + "," + vendor + ",," + description + "\n"
	encoded := utf16.Encode([]rune(text))
	buf := make([]byte, 0, len(encoded)*2)
	for _, c := range encoded {
		buf = binary.LittleEndian.AppendUint16(buf, c)
	}
	//nolint:gosec // ESP file, default perms; vfat doesn't honour mode anyway
	return fs.WriteFile(path, buf, 0o644)
}

// installRemovableFallback copies the signed shim to
// <espRoot>/EFI/BOOT/<fallbackLoader> (e.g. BOOTX64.EFI) and the
// signed grubx64.efi alongside it. The signed grub's baked-in prefix
// is absolute (`/EFI/<distro>`) so both the per-distro path and the
// removable path read the same `<ESP>/EFI/<distro>/grub.cfg` stub.
func installRemovableFallback(fs fsops.FS, espRoot, shimPath, grubPath, fallbackLoaderName string) error {
	bootDir := filepath.Join(espRoot, "EFI", "BOOT")
	if err := fs.MkdirAll(bootDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", bootDir, err)
	}
	if err := copyEFIFile(fs, shimPath, filepath.Join(bootDir, fallbackLoaderName)); err != nil {
		return fmt.Errorf("copy shim -> %s: %w", filepath.Join(bootDir, fallbackLoaderName), err)
	}
	grubName := filepath.Base(grubPath)
	if err := copyEFIFile(fs, grubPath, filepath.Join(bootDir, grubName)); err != nil {
		return fmt.Errorf("copy grub -> %s: %w", filepath.Join(bootDir, grubName), err)
	}
	// Both chained-loader names (see ensureSignedPairStaged): SUSE shims
	// load grub.efi, RH/Debian shims load grub<arch>.efi — the removable
	// dir must satisfy whichever shim was copied here as the fallback.
	if grubName != "grub.efi" {
		if err := copyEFIFile(fs, grubPath, filepath.Join(bootDir, "grub.efi")); err != nil {
			return fmt.Errorf("copy grub -> %s: %w", filepath.Join(bootDir, "grub.efi"), err)
		}
	}
	return nil
}

// copyEFIFile copies src to dst, truncating dst if it exists. The
// destination mode is 0o755 (executable on filesystems that honour
// mode bits; vfat ignores them anyway). Existing dst content is
// overwritten in place — we want to replace any earlier bad
// BOOTX64.EFI from a previous install. Mutations go through the
// dry-run-aware fsops chokepoint.
func copyEFIFile(fs fsops.FS, src, dst string) error {
	return fs.CopyFile(src, dst, 0o755)
}

// fallbackLoaderBaseName returns the unqualified filename of the
// per-arch removable boot loader (e.g. "BOOTX64.EFI" on amd64). It
// is the trailing component of the path returned by
// ArchToUEFIFallbackLoader.
func fallbackLoaderBaseName(arch string) (string, error) {
	raw, err := ArchToUEFIFallbackLoader(arch)
	if err != nil {
		return "", err
	}
	// Path uses backslashes (UEFI form); take the trailing element.
	idx := strings.LastIndex(raw, `\`)
	if idx < 0 {
		return raw, nil
	}
	return raw[idx+1:], nil
}

// pickBootFSUUID returns the UUID of /boot from the StorageResult.
// Filesystems excludes "/" (it's stored on the BootContext-level
// flow elsewhere), so /boot is what we look for. Empty string means
// the layout does not have a separate /boot mount, which is a
// configuration error for this install path.
func pickBootFSUUID(sr result.StorageResult) string {
	for _, fs := range sr.Filesystems {
		if fs.Mount == "/boot" {
			return fs.UUID
		}
	}
	return ""
}
