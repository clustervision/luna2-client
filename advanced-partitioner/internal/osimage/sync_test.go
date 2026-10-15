package osimage

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// requireGNUTar skips the test unless tar is GNU tar: the sync depends on its
// --exclude semantics and exit statuses, which a fake cannot vouch for.
func requireGNUTar(t *testing.T) {
	t.Helper()
	out, err := exec.Command("tar", "--version").Output()
	if err != nil || !strings.Contains(string(out), "GNU tar") {
		t.Skip("GNU tar not available")
	}
}

func protectionFor(preserve, empty, nonew, keep []string, skip []string) (Protection, []string) {
	pp, pe, pn, pk := ParsePatterns(preserve), ParsePatterns(empty), ParsePatterns(nonew), ParsePatterns(keep)
	ex := BuildTarExcludes(pp, pe, pn)
	for _, m := range skip {
		ex = append(ex, "--exclude=./"+m, "--exclude=./"+m+"/*")
	}
	return Protection{Preserve: pp, PreserveEmpty: pe, NoNewFiles: pn, KeepExtra: pk, SkipMounts: skip}, ex
}

// The user's requirement for the sync: the delete never removes what the tar
// exclude list protects. For every candidate path that tar excludes, the sweep
// decision must not be "remove" -- with one documented exception: the contents
// of a preserve_empty directory, which that key exists to wipe.
func TestSweepNeverDeletesTarExcluded(t *testing.T) {
	prot, ex := protectionFor(
		[]string{"var/lib/keepme", "keeptop", "etc/lpart-keep.conf", "etc/ssh/ssh_host_*", "var/log/***", "home/*/.ssh"},
		[]string{"scratch"},
		[]string{"cm/images", "opt/site"},
		[]string{"var/spool"},
		[]string{"data", "srv/data2"},
	)
	cands := []string{}
	for _, a := range []string{"var", "var/lib", "var/lib/keepme", "var/lib/keepme/m", "var/lib/keepme/sub/deep", "var/lib/other",
		"keeptop", "keeptop/x", "keeptopx", "etc", "etc/lpart-keep.conf", "etc/lpart-keep.conf.bak", "etc/ssh", "etc/ssh/ssh_host_rsa_key",
		"etc/ssh/sshd_config", "var/log", "var/log/messages", "var/log/sub/x", "home", "home/alice", "home/alice/.ssh", "home/alice/.ssh/id",
		"home/alice/data", "scratch", "scratch/x", "scratch/d/e", "cm", "cm/images", "cm/images/x", "cm/other", "opt/site", "opt/site/a",
		"opt", "var/spool", "var/spool/mail/x", "data", "data/m", "srv", "srv/data2", "srv/data2/f", "srv/other", "root", "root/m"} {
		cands = append(cands, a)
	}
	for _, rel := range cands {
		excluded := ExcludedByTar(rel, ex)
		for _, isDir := range []bool{false, true} {
			for _, inImage := range []bool{false, true} {
				act := prot.sweepAction(rel, isDir, inImage, inImage && excluded, false)
				if excluded && !prot.underEmptied(rel) && act == actRemove {
					t.Errorf("tar excludes %q but the sweep would delete it (dir=%v inImage=%v)", rel, isDir, inImage)
				}
				if prot.protectedSubtree(rel) && act != actKeepTree {
					t.Errorf("%q is protected but the sweep action is %d", rel, act)
				}
			}
		}
	}
	// and the other way round: everything preserve/no_new_files protects is
	// excluded from tar, so tar never overwrites it (keep_extra is refreshed
	// on purpose, persistent/non-os mounts are excluded as well)
	for _, rel := range []string{"var/lib/keepme/m", "keeptop/x", "etc/lpart-keep.conf", "etc/ssh/ssh_host_rsa_key", "var/log/x", "home/a/.ssh", "cm/images/x", "data/m", "srv/data2/f"} {
		if !ExcludedByTar(rel, ex) {
			t.Errorf("%q is protected from the sweep but not excluded from tar", rel)
		}
	}
	if ExcludedByTar("var/spool/x", ex) {
		t.Error("keep_extra must not be excluded from tar (its image files are refreshed)")
	}
	if !ExcludedByTar("scratch/x", ex) || ExcludedByTar("scratch", ex) {
		t.Error("preserve_empty: tar must skip the contents, not the directory")
	}
}

type ent struct {
	name, body, link string
	typ              byte
	mode             int64
}

func buildTar(t *testing.T, ents []ent) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	mt := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)
	for _, e := range ents {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: e.mode, Uid: os.Getuid(), Gid: os.Getgid(), ModTime: mt, Linkname: e.link, Format: tar.FormatPAX}
		if e.typ == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(root, rel string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(root, rel))
	return string(b), err == nil
}

func syncOpts(root string, img []byte, prot Protection, ex []string, wins []string, verify string) SyncOptions {
	return SyncOptions{
		Sysroot: root, TarballPath: "image.tar", TarExcludes: ex, Protect: prot, ImageWins: ParsePatterns(wins), Verify: verify,
		decompress: func(context.Context, string) (io.ReadCloser, func() error, error) {
			return io.NopCloser(bytes.NewReader(img)), func() error { return nil }, nil
		},
	}
}

func TestSyncExtractConverges(t *testing.T) {
	requireGNUTar(t)
	root := t.TempDir()
	d, f, l := byte(tar.TypeDir), byte(tar.TypeReg), byte(tar.TypeSymlink)
	img := buildTar(t, []ent{
		{"./", "", "", d, 0o755}, {"./etc/", "", "", d, 0o755}, {"./etc/conf", "image\n", "", f, 0o644}, {"./etc/new", "new\n", "", f, 0o644},
		{"./etc/ssh/", "", "", d, 0o755}, {"./etc/ssh/sshd_config", "image-sshd\n", "", f, 0o644},
		{"./var/", "", "", d, 0o755}, {"./var/log/", "", "", d, 0o755}, {"./var/log/imagelog", "image\n", "", f, 0o644},
		{"./var/lib/", "", "", d, 0o755}, {"./var/lib/keepme/", "", "", d, 0o755}, {"./var/lib/keepme/imgfile", "from-image\n", "", f, 0o644},
		{"./var/lib/keepme/other", "image-other\n", "", f, 0o644},
		{"./keepx/", "", "", d, 0o755}, {"./keepx/imagefile", "image\n", "", f, 0o644},
		{"./emptied/", "", "", d, 0o755}, {"./emptied/imgfile", "image\n", "", f, 0o644},
		{"./typechange", "now a file\n", "", f, 0o644},
		{"./lnk", "", "etc/conf", l, 0o777},
		{"./data/", "", "", d, 0o755},
		{"./boot/", "", "", d, 0o755}, {"./boot/vmlinuz-new", "kernel\n", "", f, 0o644},
	})
	// the disk as an earlier install left it
	write(t, root, "etc/conf", "old\n")
	write(t, root, "etc/cruft", "cruft\n")
	write(t, root, "etc/ssh/ssh_host_rsa_key", "hostkey\n")
	write(t, root, "var/log/imagelog", "local log\n")
	write(t, root, "var/log/locallog", "local\n")
	write(t, root, "var/lib/keepme/local", "keep\n")
	write(t, root, "var/lib/keepme/imgfile", "local-version\n")
	write(t, root, "var/lib/keepme/other", "local-other\n")
	write(t, root, "keepx/imagefile", "old\n")
	write(t, root, "keepx/extra", "extra\n")
	write(t, root, "emptied/old", "old\n")
	write(t, root, "typechange/inner", "dir content\n")
	write(t, root, "data/m", "persistent\n")
	write(t, root, "toplevelcruft/x", "x\n")
	write(t, root, "boot/vmlinuz-old", "old kernel\n")

	prot, ex := protectionFor(
		[]string{"/var/lib/keepme", "etc/ssh/ssh_host_*"}, []string{"emptied"}, nil, []string{"keepx"}, []string{"data"})
	// normalise like the command does
	prot.Preserve = ParsePatterns(mustNorm(t, "/var/lib/keepme", "etc/ssh/ssh_host_*"))
	ex = BuildTarExcludes(prot.Preserve, prot.PreserveEmpty, prot.NoNewFiles)
	ex = append(ex, "--exclude=./data", "--exclude=./data/*")

	so := syncOpts(root, img, prot, ex, []string{"var/lib/keepme/imgfile"}, "content")
	// tar with 512-byte records sees every header at once; with the check slowed
	// down, only the hold keeps tar from reaching the conflicting member first
	so.conflictDelay = 200 * time.Millisecond
	so.tarCmd = func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "tar", append([]string{"--blocking-factor=1"}, args...)...)
	}
	res, err := SyncExtract(context.Background(), so)
	if err != nil {
		t.Fatalf("SyncExtract: %v", err)
	}
	want := map[string]string{
		"etc/conf": "image\n", "etc/new": "new\n", "etc/ssh/sshd_config": "image-sshd\n", "etc/ssh/ssh_host_rsa_key": "hostkey\n",
		"var/log/imagelog": "image\n", "var/lib/keepme/local": "keep\n", "var/lib/keepme/imgfile": "from-image\n",
		"var/lib/keepme/other": "local-other\n", "keepx/imagefile": "image\n", "keepx/extra": "extra\n",
		"typechange": "now a file\n", "data/m": "persistent\n", "boot/vmlinuz-new": "kernel\n",
	}
	for rel, body := range want {
		if got, ok := read(root, rel); !ok || got != body {
			t.Errorf("%s = %q (present=%v), want %q", rel, got, ok, body)
		}
	}
	for _, gone := range []string{"etc/cruft", "var/log/locallog", "emptied/old", "emptied/imgfile", "toplevelcruft", "boot/vmlinuz-old", "typechange/inner"} {
		if _, err := os.Lstat(filepath.Join(root, gone)); err == nil {
			t.Errorf("%s should have been removed", gone)
		}
	}
	if fi, err := os.Stat(filepath.Join(root, "emptied")); err != nil || !fi.IsDir() {
		t.Error("the preserve_empty directory itself must stay")
	}
	if tgt, _ := os.Readlink(filepath.Join(root, "lnk")); tgt != "etc/conf" {
		t.Errorf("symlink lnk -> %q", tgt)
	}
	if res.TypeChanges != 1 || res.Removed == 0 || res.Verified == 0 || len(res.Mismatches) != 0 {
		t.Errorf("result %+v", res)
	}

	// second run on the converged tree: nothing left to remove, still verified
	res2, err := SyncExtract(context.Background(), syncOpts(root, img, prot, ex, []string{"var/lib/keepme/imgfile"}, "metadata"))
	if err != nil || res2.Removed != 0 || res2.TypeChanges != 0 {
		t.Fatalf("idempotent re-run: %+v err=%v", res2, err)
	}
}

func mustNorm(t *testing.T, raw ...string) []string {
	t.Helper()
	p, dropped := NormalizeFilterPaths(raw)
	if len(dropped) > 0 {
		t.Fatalf("dropped %v", dropped)
	}
	return p
}

// A protected directory where the image has a file must never be removed to
// make room: the sync refuses for an operator instead.
func TestSyncRefusesTypeChangeOnProtectedDir(t *testing.T) {
	requireGNUTar(t)
	root := t.TempDir()
	write(t, root, "var/lib/app/state", "precious\n")
	img := buildTar(t, []ent{{"./", "", "", tar.TypeDir, 0o755}, {"./var/", "", "", tar.TypeDir, 0o755}, {"./var/lib/", "", "", tar.TypeDir, 0o755}, {"./var/lib/app", "file now\n", "", tar.TypeReg, 0o644}})
	prot, ex := protectionFor([]string{"var/lib/app/state"}, nil, nil, nil, nil)
	_, err := SyncExtract(context.Background(), syncOpts(root, img, prot, ex, nil, "metadata"))
	if err == nil || !strings.Contains(err.Error(), "operator review required") {
		t.Fatalf("want a refusal, got %v", err)
	}
	if got, _ := read(root, "var/lib/app/state"); got != "precious\n" {
		t.Fatal("protected content was touched")
	}
}

func TestLoadFilterConfigRejectsUnknownKeys(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.json")
	_ = os.WriteFile(p, []byte(`{"preseve": ["var/log"]}`), 0o600)
	_, err := LoadFilterConfig(p)
	var fe *ErrFilterConfig
	if !errors.As(err, &fe) {
		t.Fatalf("a typo in a filter key must be a config error, got %v", err)
	}
	_ = os.WriteFile(p, []byte(`{"preserve": ["var/log"], "keep_extra": ["home"], "image_wins": ["etc/x"], "verify": "content"}`), 0o600)
	if c, err := LoadFilterConfig(p); err != nil || c.Verify != "content" || len(c.KeepExtra) != 1 {
		t.Fatalf("valid config: %+v %v", c, err)
	}
	_ = os.WriteFile(p, []byte(`{"verify": "sometimes"}`), 0o600)
	if _, err := LoadFilterConfig(p); !errors.As(err, &fe) {
		t.Fatal("an invalid verify value must be a config error")
	}
}

// A hard link in the image whose target is preserved (excluded from tar)
// must not fail the sync: it is linked to the preserved file on disk.
func TestSyncHardlinkIntoPreserved(t *testing.T) {
	requireGNUTar(t)
	root := t.TempDir()
	write(t, root, "opt/keep/bin/tool", "local tool\n")
	d, f := byte(tar.TypeDir), byte(tar.TypeReg)
	img := buildTar(t, []ent{
		{"./", "", "", d, 0o755}, {"./opt/", "", "", d, 0o755}, {"./opt/keep/", "", "", d, 0o755}, {"./opt/keep/bin/", "", "", d, 0o755},
		{"./opt/keep/bin/tool", "image tool\n", "", f, 0o755},
		{"./usr/", "", "", d, 0o755}, {"./usr/bin/", "", "", d, 0o755},
		{"./usr/bin/tool", "", "./opt/keep/bin/tool", byte(tar.TypeLink), 0o755},
		// target preserved but absent on disk: tar fails "Cannot hard link to"
		{"./opt/keep/bin/gone", "image gone\n", "", f, 0o755},
		{"./usr/bin/gone", "", "./opt/keep/bin/gone", byte(tar.TypeLink), 0o755},
	})
	prot, ex := protectionFor([]string{"opt/keep"}, nil, nil, nil, nil)
	if _, err := SyncExtract(context.Background(), syncOpts(root, img, prot, ex, nil, "metadata")); err != nil {
		t.Fatalf("SyncExtract: %v", err)
	}
	if got, _ := read(root, "usr/bin/tool"); got != "local tool\n" {
		t.Fatalf("usr/bin/tool = %q, want the preserved file", got)
	}
	a, _ := os.Stat(filepath.Join(root, "usr/bin/tool"))
	b, _ := os.Stat(filepath.Join(root, "opt/keep/bin/tool"))
	if !os.SameFile(a, b) {
		t.Fatal("usr/bin/tool is not a hard link to the preserved file")
	}
	if _, err := os.Lstat(filepath.Join(root, "usr/bin/gone")); err == nil {
		t.Fatal("usr/bin/gone links to a preserved file that is absent: it must not be created")
	}
}

// A mount below the sysroot that is not one of the os set's volumes (a data
// set without a role, anything mounted there) is never entered by the sweep,
// even where the image has a directory of the same name; os volumes converge.
func TestSyncSweepSkipsMountsOutsideTheOSSet(t *testing.T) {
	requireGNUTar(t)
	root := t.TempDir()
	mounts := filepath.Join(t.TempDir(), "mounts")
	body := "/dev/os " + root + " xfs rw 0 0\n/dev/boot " + root + "/boot xfs rw 0 0\n/dev/sdb1 " + root + "/srv/data xfs rw 0 0\n"
	if err := os.WriteFile(mounts, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	orig := procSelfMounts
	procSelfMounts = mounts
	t.Cleanup(func() { procSelfMounts = orig })

	d, f := byte(tar.TypeDir), byte(tar.TypeReg)
	img := buildTar(t, []ent{
		{"./", "", "", d, 0o755}, {"./srv/", "", "", d, 0o755}, {"./srv/data/", "", "", d, 0o755},
		{"./boot/", "", "", d, 0o755}, {"./boot/vmlinuz-new", "kernel\n", "", f, 0o644},
	})
	write(t, root, "srv/data/precious", "data set\n")
	write(t, root, "srv/data/sub/deep", "data set\n")
	write(t, root, "boot/vmlinuz-old", "old kernel\n")
	so := syncOpts(root, img, Protection{}, nil, nil, "")
	so.OSMounts = []string{"boot"}
	if _, err := SyncExtract(context.Background(), so); err != nil {
		t.Fatalf("SyncExtract: %v", err)
	}
	for _, rel := range []string{"srv/data/precious", "srv/data/sub/deep"} {
		if _, ok := read(root, rel); !ok {
			t.Errorf("%s is on a mount outside the os set and must not be swept", rel)
		}
	}
	if _, ok := read(root, "boot/vmlinuz-old"); ok {
		t.Error("boot is an os volume and must converge")
	}
}

// Verification is off unless asked for, and a mismatch it finds is a warning:
// the sync still succeeds.
func TestSyncVerifyOffByDefaultAndOnlyWarns(t *testing.T) {
	requireGNUTar(t)
	if os.Geteuid() == 0 {
		t.Skip("root tar restores the image owner, so no mismatch can be staged")
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range []*tar.Header{
		{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755, Uid: os.Getuid(), Gid: os.Getgid()},
		// a non-root tar cannot give the file this owner: verify must see it
		{Name: "./f", Typeflag: tar.TypeReg, Mode: 0o644, Uid: os.Getuid() + 1, Gid: os.Getgid(), Size: 1, ModTime: time.Unix(1e9, 0)},
	} {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	res, err := SyncExtract(context.Background(), syncOpts(t.TempDir(), buf.Bytes(), Protection{}, nil, nil, ""))
	if err != nil || res.Verified != 0 {
		t.Fatalf("default: want no verification, got %+v err=%v", res, err)
	}
	res, err = SyncExtract(context.Background(), syncOpts(t.TempDir(), buf.Bytes(), Protection{}, nil, nil, "metadata"))
	if err != nil {
		t.Fatalf("a verify mismatch must not fail the sync: %v", err)
	}
	if len(res.Mismatches) == 0 {
		t.Fatalf("the owner mismatch was not reported: %+v", res)
	}
}

// The hard-link tolerance holds only for tar's own exit status 2. A tar that
// did not exit (killed, as by the OOM killer) failed, whatever its stderr says.
func TestSyncHardlinkToleranceNeedsExitStatus2(t *testing.T) {
	requireGNUTar(t)
	root := t.TempDir()
	d, f := byte(tar.TypeDir), byte(tar.TypeReg)
	img := buildTar(t, []ent{
		{"./", "", "", d, 0o755}, {"./opt/", "", "", d, 0o755}, {"./opt/keep/", "", "", d, 0o755},
		{"./opt/keep/gone", "image\n", "", f, 0o644},
		{"./usr/", "", "", d, 0o755}, {"./usr/gone", "", "./opt/keep/gone", byte(tar.TypeLink), 0o644},
	})
	prot, ex := protectionFor([]string{"opt/keep"}, nil, nil, nil, nil)
	so := syncOpts(root, img, prot, ex, nil, "")
	// the real tar runs to the end, then the process dies by a signal
	so.tarCmd = func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", append([]string{"-c", `tar "$@"; kill -9 $$`, "sh"}, args...)...) //nolint:gosec // test-built args
	}
	_, err := SyncExtract(context.Background(), so)
	if err == nil || !strings.Contains(err.Error(), "signal: killed") || !strings.Contains(err.Error(), "Cannot hard link") {
		t.Fatalf("a killed tar must fail the sync, got %v", err)
	}
}
