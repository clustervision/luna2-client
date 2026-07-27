---
title: "Luna Node Installer"
confluence:
  id: '981499912'
  space: TRIX
  version: 2
  type: page
---

# Luna Node Installer

> **Start here.** This is the developer documentation for how a TrinityX/Luna node goes from
> power-on to a running operating system — the node-side install path and the controller data that
> drives it.

## Why this exists

Cluster provisioning has changed its language. The Kubernetes generation expects infrastructure to be **declarative**: you state the desired end result and the system makes reality match it, machines are interchangeable rather than precious, operating systems ship as images, and systems are expected to heal themselves. Bare metal has followed the same path through Metal3, Cluster API, Talos and Canonical MAAS, and the build-out of AI factories — NVIDIA DGX SuperPOD, Base Command Manager — has raised the bar to thousands of GPU nodes provisioned from golden images.

The Luna Node Installer brings that modern, declarative style to TrinityX. A node states a **desired state** — one `install_mode` field — and the installer makes the node match it: install if blank, refresh if healthy, repair if broken, **never touch declared data**. Disks are chosen by **tags** (the bare-metal equivalent of a label selector) rather than device paths, so one group definition fits a whole fleet of varying hardware. The whole thing is a small set of Go binaries driven from one distro-agnostic JSON document, with **no kickstart, AutoYaST or Preseed** anywhere.

**The one idea to carry through:** behind every field, a node's install is only ever *desired-state in, healed-state out* — and a node's disks are only ever *discover, pin, or manual*.

## Documentation map

| Page | What it covers | Last verified |
|---|---|---|
| **01 — Architecture Overview** | The boot→install→first-boot shape; where we stand vs the field; the highlights | 10 Jun 2026 |
| **02 — Module Map** | The `95luna` dracut module, the `lpart-*` Go binaries, the daemon touch-points | 10 Jun 2026 |
| **03 — Data Flows** | The install lifecycle end to end: what the daemon produces, what the node consumes | 10 Jun 2026 |
| **04 — Integrations** | Daemon REST, the file server, PXE/iPXE, the TPM token, SSH-into-installer | 10 Jun 2026 |
| **05 — Dependencies** | The Go module, the initramfs toolset, and the per-distro package requirements | 10 Jun 2026 |
| **06 — Configuration Reference** | `install_mode`, `disklayout` (the set-of-sets), `osimage_filter`, the kernel command line | 10 Jun 2026 |
| **07 — Testing & Quality** | The gates, the test layers, what is hardware-proven vs wired-not-yet-proven | 10 Jun 2026 |
| **08 — Operations & Runbook** | Force-PXE, the install-log stream, `osimage pack`, recovery | 10 Jun 2026 |
| **09 — Security Model** | The TPM token, SSH-into-installer, secret redaction, UEFI/Secure-Boot posture | 10 Jun 2026 |
| **10 — Gaps & Risks** | The honest gaps and the roadmap | 10 Jun 2026 |
| **11 — Comparison & Prior Art** | How this differs from MAAS, Bright/BCM and xCAT | 10 Jun 2026 |

## Reading paths

- **New developer:** 01 → 02 → 03 → 07
- **Operator:** 08 → 06 → 04
- **Reviewer / architect / evaluator:** 01 → 11 → 03 → 06 → 10

## Conventions

- Pages document the **current state** of the installer — what it *is*, not its history.
- Every factual claim is anchored to a real file, field, or command in
  `clustervision/luna2-client` (the node-side client + `advanced-partitioner`) or
  `clustervision/luna2-daemon` (the controller side).
- Diagram sources live alongside the docs as Mermaid `.mmd`; propose changes by merge request, not a live edit.

---
*Generated against `luna2-client@advanced-partitioner` on 10 Jun 2026.*
