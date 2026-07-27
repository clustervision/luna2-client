package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/cli"
	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/lunaclient"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/osimage"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/result"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/sysenv"
)

// writeBackParams bundles everything maybeWriteBack needs. All external inputs
// are fields so the whole flow is testable without touching /proc or the
// network (decisions/0008).
type writeBackParams struct {
	cfg      *v2.Config
	resolved []result.ResolvedSet
	dryRun   bool

	cmdlinePath   string // /proc/cmdline source for luna.url / luna.node
	tokenSource   string // install.sh path the token is read from
	tokenOverride string // explicit token (tests / env); wins over tokenSource
	insecure      bool   // skip controller TLS verify (self-signed cert)

	// pin performs the POST; nil → lunaclient.PinDisklayout. Injected in tests.
	pin    func(context.Context, lunaclient.PinParams) error
	now    func() time.Time // nil → time.Now
	logger *txlog.Logger
}

// maybeWriteBack pins the resolved layout to the node-level disklayout when a
// selection=discover set asked for it (save:true). It is deliberately
// non-fatal: every failure path logs and returns an error for the caller to
// surface, but the install is not aborted — the data is already on disk and the
// paste-ready block was already emitted as the manual fallback (decisions/0008).
func maybeWriteBack(ctx context.Context, p writeBackParams) error {
	if !anySetSaves(p.cfg) {
		return nil // no discover set asked to be saved; 0007 paste block only
	}
	now := p.now
	if now == nil {
		now = time.Now
	}

	// The controller URL and node name come from the kernel cmdline (the same
	// luna.url=/luna.node= the bash client reads). luna.node is the authoritative
	// node identity, so it labels both the write-back target and the origin stamp.
	cmdline := sysenv.Options{CmdlinePath: p.cmdlinePath}
	baseURL, ok := sysenv.KernelParam(cmdline, "luna.url")
	if !ok || baseURL == "" {
		return fmt.Errorf("cannot pin: luna.url missing from kernel cmdline")
	}
	node, ok := sysenv.KernelParam(cmdline, "luna.node")
	if !ok || node == "" {
		return fmt.Errorf("cannot pin: luna.node missing from kernel cmdline")
	}

	origin := &v2.Origin{
		From: "discover",
		At:   now().UTC().Format(time.RFC3339),
		By:   toolName + " " + cli.Version(),
		Host: node,
	}
	pinned := buildPinnableConfig(p.cfg, p.resolved, origin)
	if pinned == nil {
		return fmt.Errorf("save requested but no set resolved to concrete disks; nothing to pin")
	}
	if err := pinned.Validate(); err != nil {
		return fmt.Errorf("refusing to pin an invalid layout: %w", err)
	}
	body, err := json.Marshal(pinned)
	if err != nil {
		return fmt.Errorf("encode pinned layout: %w", err)
	}

	token := p.tokenOverride
	if token == "" {
		token, err = osimage.DetectLunaToken(osimage.DetectOptions{LunaInstallScript: p.tokenSource})
		if err != nil {
			return fmt.Errorf("cannot pin: %w", err)
		}
	}

	params := lunaclient.PinParams{
		BaseURL:    baseURL,
		Node:       node,
		Token:      token,
		Disklayout: body,
		Insecure:   p.insecure,
	}

	if p.dryRun {
		// The token is only ever a curl header arg, never in the URL or body,
		// so this log line carries no secret.
		p.logger.UserInfo("dry-run: would POST resolved layout to %s", params.Endpoint())
		p.logger.UserInfo("dry-run: disklayout body: %s", string(body))
		return nil
	}

	doPin := p.pin
	if doPin == nil {
		doPin = lunaclient.PinDisklayout
	}
	p.logger.SetStage("pin_disklayout")
	p.logger.UserInfo("save: pinning resolved layout to node %q at %s", node, params.Endpoint())
	if err := doPin(ctx, params); err != nil {
		return err
	}
	p.logger.UserInfo("save: node %q disklayout pinned (%d set(s)); next boot reproduces this layout", node, len(pinned.Sets))
	return nil
}
