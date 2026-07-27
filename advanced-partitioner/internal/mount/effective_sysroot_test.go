package mount

import "testing"

// EffectiveSysroot must target where the *running* initramfs pivots:
// initramfs-tools (Ubuntu) exports rootmnt=/root and run-inits into it, so a
// dracut-pinned /sysroot would leave /root empty ("No init found"). dracut
// leaves rootmnt unset and uses /sysroot.

func TestEffectiveSysrootHonoursRootmnt(t *testing.T) {
	t.Setenv("rootmnt", "/root")
	if got := EffectiveSysroot(); got != "/root" {
		t.Fatalf("EffectiveSysroot() with rootmnt=/root = %q, want /root", got)
	}
}

func TestEffectiveSysrootDefaultsToSysrootWhenUnset(t *testing.T) {
	t.Setenv("rootmnt", "") // treated as unset (dracut)
	if got := EffectiveSysroot(); got != DefaultSysroot {
		t.Fatalf("EffectiveSysroot() with rootmnt unset = %q, want %q", got, DefaultSysroot)
	}
}

func TestNewMounterDefaultsToEffectiveSysroot(t *testing.T) {
	t.Setenv("rootmnt", "/root")
	if m := New(Options{}); m.o.Sysroot != "/root" {
		t.Fatalf("New(Options{}).Sysroot = %q, want /root (from rootmnt)", m.o.Sysroot)
	}
}
