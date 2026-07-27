package bootloader

import (
	"context"
	"fmt"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fsops"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
)

// Adapter is the family-specific bootloader interface. lpart-bootloader-
// finalise calls Prepare → RegenerateInitramfs → Install → Validate
// in that order; failure at any step aborts. Mirrors
// bootloader_dispatch in lib/plugin_registry.sh.
type Adapter interface {
	// Family identifies which family this adapter speaks for.
	Family() Family

	// Prepare checks that the bootloader tools the family needs
	// (dracut/grub2-install/grub2-mkconfig for RHEL, update-initramfs
	// /grub-install/update-grub for Debian) exist under /sysroot,
	// and validates that the required mount points (/boot, /boot/efi)
	// are in place.
	Prepare(ctx context.Context) error

	// RegenerateInitramfs runs the distro's initramfs builder for the
	// target kernel inside sysroot via chroot.
	RegenerateInitramfs(ctx context.Context) error

	// Install runs the actual grub-install + grub-mkconfig (or family
	// equivalents) inside the sysroot chroot and, for UEFI, registers
	// the new NVRAM boot entries.
	Install(ctx context.Context) error

	// Validate checks the bootloader install left the expected files
	// behind (grub.cfg, EFI dir for UEFI). The bash predecessor runs
	// this as the very last step.
	Validate(ctx context.Context) error
}

// AdapterDeps holds the runtime collaborators every adapter needs.
type AdapterDeps struct {
	Runner      executor.Runner
	Logger      *txlog.Logger
	BootContext BootContext
	// FS is the dry-run-aware native filesystem chokepoint. The zero
	// value performs real operations, so omitting it in tests is safe.
	FS fsops.FS

	// SnapshotDir, when non-empty, is where efibootmgr -v output is
	// captured before/after BootOrder reordering. Defaults are
	// supplied by the cmd binary.
	SnapshotDir string
}

// New returns the adapter for the family in ctx, or an error when the
// family is unknown. The Adapter is opaque after construction; callers
// drive it via the interface.
func New(deps AdapterDeps) (Adapter, error) {
	switch deps.BootContext.Family {
	case FamilyRHEL:
		return &rhelAdapter{deps: deps}, nil
	case FamilyDebian:
		return &debianAdapter{deps: deps}, nil
	case FamilySUSE:
		return newSUSEAdapter(deps), nil
	case FamilyNone:
		return &noopAdapter{deps: deps}, nil
	default:
		return nil, fmt.Errorf("unsupported bootloader family: %s", deps.BootContext.Family)
	}
}

// noopAdapter is the FamilyNone path: every step returns nil. Used by
// tmpfs roots where the OS image (and therefore any bootloader binary)
// is never extracted.
type noopAdapter struct{ deps AdapterDeps }

func (a *noopAdapter) Family() Family                              { return FamilyNone }
func (a *noopAdapter) Prepare(_ context.Context) error             { return nil }
func (a *noopAdapter) RegenerateInitramfs(_ context.Context) error { return nil }
func (a *noopAdapter) Install(_ context.Context) error             { return nil }
func (a *noopAdapter) Validate(_ context.Context) error            { return nil }
