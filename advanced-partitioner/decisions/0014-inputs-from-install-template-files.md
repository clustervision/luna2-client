# 0014 — lpart sources provisioning inputs from the install-template files, not a node-record GET

Date: 2026-08-28 · Status: accepted

## Context
`internal/lpartinputs.Resolve` fetched the node's inputs (`install_mode`, `disklayout`,
`osimage_filter`, `provision_*`) with `GET /config/node/<node>` as its primary source, falling
back to the files Luna's install template already wrote into the runtime dir. But the daemon
guards `GET /config/node/<node>` with the admin-only `token_required` — it rejects the scoped
provision token a booting node holds — so the primary path returned 403 on every real install and
the fallback did all the work unseen. The values it wants are group→node *merged* fields the
daemon already resolves and renders into `/boot/install` (provision-scoped), which the template
drops as `storage.json` / `osimage-filter.json` / `install-context.json` before any lpart phase.
So the GET both never worked here and duplicated data the node already held — while asking the
provision token to reach the entire node record.

## Decision
Drop the node-record GET. `Resolve` reads solely from the runtime files the install template
materialises; `lpartinputs.Resolve` remains their sole normaliser (ADR-0002 holds). The install
template is extended to also write `provision_method/fallback/interface` into
`install-context.json` (it already wrote `install_mode`), so the download phase honours a node's
overrides instead of silently defaulting. `GET /config/node/<node>` stays admin-only. For anything
that does want the layout from the daemon, the disklayout is now exposed on its own,
provision-scoped and node-matched, at `GET /config/node/<node>/disklayout`.

## Consequences
The provision token no longer reaches the full node record — only its own disklayout, and only via
the narrow route. Inputs come from a snapshot rendered at `/boot/install` time rather than a live
read, which is acceptable: both derive from the same merged daemon values moments apart. Removed
the daemon-fetch code and its tests; `provision_*` passthrough is pinned by the updated
`TestResolveFromFiles`, and the daemon side by `test_install_template_parity` (the template writes
the fields) and `test_provision_token_scope` (disklayout scoped, full record admin-only).
