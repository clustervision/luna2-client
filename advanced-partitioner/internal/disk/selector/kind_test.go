package selector

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func kindOf(t *testing.T, err error) ErrorKind {
	t.Helper()
	var re *ResolveError
	if !errors.As(err, &re) {
		t.Fatalf("want *ResolveError, got %T: %v", err, err)
	}
	return re.Kind
}

// The resolver classifies its failures so lpart-storage-prepare can keep the
// exit contract: a missing disk and a read-only / non-whole disk are
// deterministic (park), a mounted disk is a refusal, a probe error may clear.
func TestResolveErrorKinds(t *testing.T) {
	missing := "/dev/disk/by-id/lpart-test-no-such-disk-" + filepath.Base(t.TempDir())
	r := New(Options{Runner: newFakeRunner(), IsBlockDevice: stubBlock()})
	_, err := r.Resolve(context.Background(), missing)
	if kindOf(t, err) != KindNotFound || !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing device must be KindNotFound / ErrNotFound: %v", err)
	}
	var re *ResolveError
	if errors.As(err, &re); re.Device != missing {
		t.Fatalf("error must name the configured device, got %q", re.Device)
	}

	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skip("/dev/null required")
	}
	dev := "/dev/null"
	for _, c := range []struct {
		name  string
		setup func() *Resolver
		want  ErrorKind
	}{
		{"read-only", func() *Resolver {
			f := newFakeRunner()
			addLsblkScalars(f, dev, "disk", "1", "1", "M", "S")
			addFindmntNotMounted(f, dev)
			return New(Options{Runner: f, IsBlockDevice: stubBlock(dev)})
		}, KindInvalid},
		{"partition", func() *Resolver {
			f := newFakeRunner()
			addLsblkScalars(f, dev, "part", "0", "1", "M", "S")
			return New(Options{Runner: f, IsBlockDevice: stubBlock(dev)})
		}, KindInvalid},
		{"mounted", func() *Resolver {
			f := newFakeRunner()
			addLsblkScalars(f, dev, "disk", "0", "1", "M", "S")
			addFindmntMounted(f, dev)
			return New(Options{Runner: f, IsBlockDevice: stubBlock(dev)})
		}, KindInUse},
		{"probe-error", func() *Resolver {
			f := newFakeRunner()
			addLsblkScalars(f, dev, "disk", "0", "1", "M", "S")
			addInUseProbeError(f, dev)
			return New(Options{Runner: f, IsBlockDevice: stubBlock(dev)})
		}, KindProbe},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.setup().Resolve(context.Background(), dev)
			if got := kindOf(t, err); got != c.want {
				t.Fatalf("kind = %d, want %d (%v)", got, c.want, err)
			}
			if errors.Is(err, ErrNotFound) {
				t.Fatal("only a missing device matches ErrNotFound")
			}
		})
	}
}
