package osimage

// Streaming HTTP extract (FTA v2 BE-G5, barrier NB-6): `curl | tar` in one
// pipe, never materialising the tarball. The classic Download+Extract path
// holds BOTH the tarball and the extracted tree at peak — and /tmp in the
// installer initramfs is tmpfs, so that peak is RAM even for disk installs.
// A 4.4GB image therefore needed >8.8GB free RAM on memboot (observed live:
// an 8GB node stalled mid-extract). Streaming halves the peak to the
// extracted tree alone.
//
// Scope: HTTP(S) only — torrent needs the file. The caller restricts
// streaming to tmpfs sysroots by default (memboot, where the win is decisive
// and a mid-stream network failure costs only a retry on a fresh RAM root)
// and falls back to the classic path on any stream failure, preserving the
// "payload in hand before anything irreversible" ordering for disk installs.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// StreamHTTPExtract downloads ImageFile over HTTP and extracts it into
// eopts.Sysroot in a single `curl | tar` pipe. dopts supplies the URL,
// auth token, TLS policy, NIC pinning and watchdog; eopts supplies the
// sysroot, excludes and ACL mode (TarballPath is ignored). The pipe runs
// under `set -o pipefail`, so a curl failure fails the whole step even
// when tar exits 0 on the truncated stream.
//
// curl's --retry is deliberately OMITTED here (unlike downloadHTTP): a
// mid-stream retry would restart the byte stream into a tar that already
// consumed part of it, silently corrupting the extract. Retrying is the
// caller's job (fall back to the classic download+extract).
func StreamHTTPExtract(ctx context.Context, dopts DownloadOptions, eopts ExtractOptions) error {
	if dopts.Runner == nil {
		return fmt.Errorf("osimage.StreamHTTPExtract: Runner is required")
	}
	if dopts.ImageFile == "" {
		return fmt.Errorf("osimage.StreamHTTPExtract: ImageFile is required")
	}
	if dopts.LunaToken == "" {
		return fmt.Errorf("osimage.StreamHTTPExtract: LunaToken is required")
	}
	if eopts.Sysroot == "" {
		return fmt.Errorf("osimage.StreamHTTPExtract: Sysroot is required")
	}
	if dopts.LunaFileBaseURL == "" {
		dopts.LunaFileBaseURL = DefaultLunaFileBaseURL
	}
	imageURL := dopts.LunaFileBaseURL + "/" + dopts.ImageFile

	curlArgs := []string{
		"--connect-timeout", "30",
		"--speed-limit", "1024", "--speed-time", "120",
		// no --retry: see the function comment.
	}
	if dopts.Insecure {
		curlArgs = append(curlArgs, "--insecure")
	}
	if dopts.LunaInterface != "" {
		curlArgs = append(curlArgs, "--interface", dopts.LunaInterface)
	}
	curlArgs = append(curlArgs, "-f", "-L", "-sS",
		"-H", "x-access-tokens: "+dopts.LunaToken, imageURL)

	tarArgs := []string{"-I", "lbzip2"}
	if eopts.ACLs {
		tarArgs = append(tarArgs, "--acls")
	} else {
		tarArgs = append(tarArgs, "--no-acls")
	}
	tarArgs = append(tarArgs, "--numeric-owner")
	tarArgs = append(tarArgs, eopts.TarExcludes...)
	tarArgs = append(tarArgs, "-xf", "-", "-C", eopts.Sysroot)

	pipe := "set -o pipefail; curl " + shJoin(curlArgs) + " | tar " + shJoin(tarArgs)

	dopts.info("streaming osimage extract (curl | tar): " + imageURL + " -> " + eopts.Sysroot)
	if _, err := dopts.Runner.Run(ctx, executor.CommandSpec{
		Path: "sh", Args: []string{"-c", pipe},
		CaptureStdout: true, CaptureStderr: true,
	}); err != nil {
		return fmt.Errorf("streamed curl|tar extract: %w", err)
	}

	osRelease := filepath.Join(eopts.Sysroot, "etc", "os-release")
	if _, err := os.Stat(osRelease); err != nil {
		return fmt.Errorf("streamed extract did not produce %s: %w", osRelease, err)
	}
	dopts.info("osimage streamed into " + eopts.Sysroot + " successfully")
	return nil
}

// shJoin single-quotes each argument for safe embedding in an `sh -c`
// command line (POSIX quoting: close, escaped quote, reopen).
func shJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}
