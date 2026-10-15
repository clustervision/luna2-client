package osimage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
)

// DefaultLunaFileBaseURL is the last-resort OS-image file-server endpoint
// used only when the URL cannot be derived from the kernel cmdline. The
// real base URL is derived from luna.url via FileBaseFromLunaURL; this
// constant must not be relied on in production (the controller IP varies).
const DefaultLunaFileBaseURL = "http://10.141.255.254:7051/files"

// FileServerPort is the controller's nginx OS-image file server port.
const FileServerPort = "7051"

// FileBaseFromLunaURL derives the OS-image file-server base URL from the
// daemon URL on the kernel cmdline. luna.url is the daemon (e.g.
// "https://10.141.255.252:7050"); the file server is nginx on the same
// controller host at http://<host>:7051/files. Returns "" if no host parses.
func FileBaseFromLunaURL(lunaURL string) string {
	u := strings.TrimSpace(lunaURL)
	u = strings.TrimPrefix(u, "https://")
	u = strings.TrimPrefix(u, "http://")
	if i := strings.IndexByte(u, '/'); i >= 0 {
		u = u[:i]
	}
	host := u
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	return "http://" + host + ":" + FileServerPort + "/files"
}

// DownloadOptions describes one OS-image download attempt.
type DownloadOptions struct {
	Runner executor.Runner
	Logger *txlog.Logger
	// LunaFileBaseURL is the HTTP base, e.g.
	// "http://10.141.255.254:7051/files". Trailing slash is optional.
	LunaFileBaseURL string
	// LunaToken is the bearer token passed in the x-access-tokens
	// header.
	LunaToken string
	// LunaInterface, when non-empty, becomes `curl --interface <name>`.
	LunaInterface string
	// ImageFile is the tarball name on the file server, e.g.
	// "compute-image-1779269535.tar.bz2".
	ImageFile string
	// CacheDir is where the tarball lands on the node. Defaults to /tmp.
	CacheDir string
	// LogDir is where aria2c.out is captured. Defaults to /tmp.
	LogDir string
	// Aria2cAvailable controls torrent attempts. Defaults to a real
	// exec.LookPath probe.
	Aria2cAvailable func() bool
	// ProvisionMethod / ProvisionFallback are the daemon-resolved
	// download order (e.g. "torrent" then "http"), from the node's
	// provision_method/provision_fallback. The Go installer MUST honour
	// these rather than freezing a default — they are the same fields the
	// legacy install.sh consumes via download_{{PROVISION_METHOD}} /
	// download_{{PROVISION_FALLBACK}}. Empty => the historical
	// torrent-then-http default.
	ProvisionMethod   string
	ProvisionFallback string
	// Insecure skips TLS verification on the curl fetches when the file
	// server is HTTPS (luna.verifycert=false), mirroring every other Luna
	// caller (lpartinputs/lunaclient/writeback).
	Insecure bool
}

// Download fetches the image into CacheDir/ImageFile using aria2c
// (torrent) when available, falling back to HTTP via curl. The
// returned path is the local file. Mirrors download_osimage_with_torrent
// + download_osimage_with_http in bin/lpart-osimage-install.
func Download(ctx context.Context, opts DownloadOptions) (string, error) {
	if opts.Runner == nil {
		return "", fmt.Errorf("osimage.Download: Runner is required")
	}
	if opts.ImageFile == "" {
		return "", fmt.Errorf("osimage.Download: ImageFile is required")
	}
	if opts.LunaToken == "" {
		return "", fmt.Errorf("osimage.Download: LunaToken is required")
	}
	if opts.LunaFileBaseURL == "" {
		opts.LunaFileBaseURL = DefaultLunaFileBaseURL
	}
	if opts.CacheDir == "" {
		opts.CacheDir = "/tmp"
	}
	if opts.LogDir == "" {
		opts.LogDir = "/tmp"
	}
	if opts.Aria2cAvailable == nil {
		opts.Aria2cAvailable = defaultAria2cAvailable
	}

	cachePath := filepath.Join(opts.CacheDir, opts.ImageFile)
	imageURL := opts.LunaFileBaseURL + "/" + opts.ImageFile

	// Honour the daemon-provided provision method order. download_image in
	// the legacy template does exactly this (download_<method> then
	// download_<fallback>); the Go port must not hardcode torrent-first.
	var lastErr error
	for _, method := range ProvisionOrder(opts.ProvisionMethod, opts.ProvisionFallback) {
		switch method {
		case "torrent":
			if !opts.Aria2cAvailable() {
				opts.info("torrent requested but aria2c not present; skipping")
				continue
			}
			if err := downloadTorrent(ctx, opts, cachePath); err != nil {
				lastErr = err
				opts.warn("torrent download failed: " + err.Error())
				continue
			}
			return cachePath, nil
		case "http", "https":
			if err := downloadHTTP(ctx, opts, imageURL, cachePath); err != nil {
				lastErr = err
				opts.warn("http download failed: " + err.Error())
				continue
			}
			return cachePath, nil
		default:
			opts.warn("unknown provision method " + method + "; skipping")
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no usable provision method (method=%q fallback=%q)",
			opts.ProvisionMethod, opts.ProvisionFallback)
	}
	return "", fmt.Errorf("all provision methods failed: %w", lastErr)
}

// ProvisionOrder returns the ordered, de-duplicated, lower-cased list of
// download methods to try: the primary method then the fallback. An empty
// primary defaults to the historical torrent-then-http behaviour so a node
// with no provision fields still works.
func ProvisionOrder(method, fallback string) []string {
	norm := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	m, f := norm(method), norm(fallback)
	if m == "" && f == "" {
		return []string{"torrent", "http"}
	}
	out := []string{}
	for _, v := range []string{m, f} {
		if v == "" {
			continue
		}
		dup := false
		for _, e := range out {
			if e == v {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, v)
		}
	}
	// http is always the last resort: torrent alone (and no aria2c in the
	// initramfs) is a permanent download failure otherwise.
	if !slices.Contains(out, "http") && !slices.Contains(out, "https") {
		out = append(out, "http")
	}
	return out
}

// downloadTorrent fetches <image>.torrent via curl, then runs aria2c
// inside opts.CacheDir to materialise the tarball alongside.
func downloadTorrent(ctx context.Context, opts DownloadOptions, cachePath string) error {
	torrentFile := opts.ImageFile + ".torrent"
	torrentPath := filepath.Join(opts.CacheDir, torrentFile)
	torrentURL := opts.LunaFileBaseURL + "/" + torrentFile

	opts.info("fetching torrent metadata: " + torrentURL)
	curlArgs := append(curlBaseArgs(opts),
		"-H", "x-access-tokens: "+opts.LunaToken,
		"-f", "-s", "-o", torrentPath, torrentURL, // -f: a 401/404 body is not a torrent
	)
	if _, err := opts.Runner.Run(ctx, executor.CommandSpec{
		Path: "curl", Args: curlArgs,
		CaptureStdout: true, CaptureStderr: true,
	}); err != nil {
		return fmt.Errorf("curl torrent: %w", err)
	}
	defer func() { _ = os.Remove(torrentPath) }()

	if _, err := opts.Runner.Run(ctx, executor.CommandSpec{
		Path: "aria2c",
		Args: []string{
			"--seed-time=0",
			"--enable-dht=false",
			"--max-connection-per-server=2",
			"--bt-stop-timeout=120",
			"--check-certificate=false",
			"--dir=" + opts.CacheDir,
			torrentFile,
		},
		WorkDir:       opts.CacheDir,
		CaptureStdout: true, CaptureStderr: true,
	}); err != nil {
		return fmt.Errorf("aria2c: %w", err)
	}
	if _, err := os.Stat(cachePath); err != nil {
		return fmt.Errorf("aria2c finished but %s is missing: %w", cachePath, err)
	}
	return nil
}

// downloadHTTP issues a single curl GET with the Luna auth header.
func downloadHTTP(ctx context.Context, opts DownloadOptions, imageURL, cachePath string) error {
	opts.info("HTTP osimage download: " + imageURL)
	args := append(curlBaseArgs(opts),
		"-f", "-L",
		"-H", "x-access-tokens: "+opts.LunaToken,
		imageURL, "-o", cachePath,
	)
	if _, err := opts.Runner.Run(ctx, executor.CommandSpec{
		Path: "curl", Args: args,
		CaptureStdout: true, CaptureStderr: true,
	}); err != nil {
		return fmt.Errorf("curl: %w", err)
	}
	if _, err := os.Stat(cachePath); err != nil {
		return fmt.Errorf("HTTP download claimed success but %s is missing: %w", cachePath, err)
	}
	return nil
}

// curlBaseArgs builds the leading curl args common to every Luna fetch:
// --insecure when TLS verification is disabled, --interface to pin the
// provisioning NIC, and the WATCHDOG flags. Without a watchdog a Luna
// server that accepts the connection and then stalls hangs the node
// forever — and at fleet scale, every node at once (ancestor FTA CC-6;
// the fix was lost in the port and is pinned by a test now):
//
//	--connect-timeout 30                  fail fast when unreachable;
//	--speed-limit 1024 --speed-time 120   abort when the transfer drops
//	                                      below 1 KB/s for 2 minutes
//	                                      (stall detector that still
//	                                      tolerates slow links);
//	--retry 3 --retry-delay 5             ride out transient hiccups.
func curlBaseArgs(opts DownloadOptions) []string {
	args := []string{
		"--connect-timeout", "30",
		"--speed-limit", "1024", "--speed-time", "120",
		"--retry", "3", "--retry-delay", "5",
	}
	if opts.Insecure {
		args = append(args, "--insecure")
	}
	if opts.LunaInterface != "" {
		args = append(args, "--interface", opts.LunaInterface)
	}
	return args
}

func defaultAria2cAvailable() bool {
	// We let the orchestrator decide via exec.LookPath; here we just
	// say "no" so the package's default behaviour is the conservative
	// HTTP-only path. cmd/lpart-osimage-install overrides this with a
	// real probe.
	return false
}

func (o DownloadOptions) info(msg string) {
	if o.Logger != nil {
		o.Logger.Info(msg)
	}
}

func (o DownloadOptions) warn(msg string) {
	if o.Logger != nil {
		o.Logger.Warn(msg)
	}
}
