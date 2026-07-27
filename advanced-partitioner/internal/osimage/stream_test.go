package osimage

// Pinning tests for the streamed curl|tar extract (FTA v2 BE-G5, barrier
// NB-6). Invariants: single sh -c pipe under pipefail (a curl failure must
// fail the step even when tar tolerates truncation); NO curl --retry inside
// the pipe (a mid-stream retry would corrupt the tar stream); the tarball
// never touches disk (tar reads stdin); the os-release proof still gates
// success.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

func streamOpts(sysroot string) (DownloadOptions, ExtractOptions) {
	d := DownloadOptions{
		LunaFileBaseURL: "http://ctrl:7051/files",
		LunaToken:       "tok",
		LunaInterface:   "eth1",
		ImageFile:       "img.tar.bz2",
		Insecure:        true,
	}
	e := ExtractOptions{
		Sysroot:     sysroot,
		TarExcludes: []string{"--exclude", "./proc/*"},
		ACLs:        false,
	}
	return d, e
}

func TestStreamHTTPExtractPipeShape(t *testing.T) {
	sysroot := t.TempDir()
	// os-release must exist post-"extract" for the proof to pass (the fake
	// runner does not actually extract anything).
	if err := os.MkdirAll(filepath.Join(sysroot, "etc"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sysroot, "etc", "os-release"), []byte("ID=test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := executor.NewFakeRunner(executor.FakeResponse{})
	d, e := streamOpts(sysroot)
	d.Runner = f
	if err := StreamHTTPExtract(context.Background(), d, e); err != nil {
		t.Fatalf("StreamHTTPExtract: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 1 || calls[0].Path != "sh" {
		t.Fatalf("expected exactly one sh invocation, got %+v", calls)
	}
	cmd := strings.Join(calls[0].Args, " ")
	for _, want := range []string{
		"set -o pipefail",
		"curl",
		"| tar",
		"'-xf' '-'", // tar reads stdin — no tarball on disk
		"'-C' '" + sysroot + "'",
		"x-access-tokens: tok",
		"'--interface' 'eth1'",
		"'--insecure'",
		"'--speed-limit' '1024'", // the stall watchdog survives in the pipe
		"'--no-acls'",
		"'--exclude' './proc/*'",
		"http://ctrl:7051/files/img.tar.bz2",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("pipe command missing %q:\n%s", want, cmd)
		}
	}
	if strings.Contains(cmd, "--retry") {
		t.Fatalf("curl --retry must NOT appear inside the pipe (mid-stream retry corrupts tar):\n%s", cmd)
	}
	if strings.Contains(cmd, "'-o'") {
		t.Fatalf("curl must stream to stdout, not -o a file:\n%s", cmd)
	}
}

func TestStreamHTTPExtractRequiresOSRelease(t *testing.T) {
	sysroot := t.TempDir() // no etc/os-release -> proof must fail
	f := executor.NewFakeRunner(executor.FakeResponse{})
	d, e := streamOpts(sysroot)
	d.Runner = f
	err := StreamHTTPExtract(context.Background(), d, e)
	if err == nil || !strings.Contains(err.Error(), "os-release") {
		t.Fatalf("missing os-release must fail the streamed extract, got: %v", err)
	}
}

func TestStreamHTTPExtractFailedPipeSurfaces(t *testing.T) {
	sysroot := t.TempDir()
	f := executor.NewFakeRunner(executor.FakeResponse{ExitCode: 22, Err: fmt.Errorf("exit status 22")})
	d, e := streamOpts(sysroot)
	d.Runner = f
	if err := StreamHTTPExtract(context.Background(), d, e); err == nil {
		t.Fatal("a failing pipe must surface as an error (caller falls back to classic)")
	}
}

func TestShJoinQuoting(t *testing.T) {
	got := shJoin([]string{"a b", "it's", "plain"})
	want := `'a b' 'it'\''s' 'plain'`
	if got != want {
		t.Fatalf("shJoin = %s, want %s", got, want)
	}
}
