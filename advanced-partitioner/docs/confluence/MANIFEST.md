# Luna Node Installer — Confluence doc set

The developer documentation for the Luna node installer, published in Confluence space **TRIX**
under the parent page **Luna Node Installer**. Generated with the `confluence-devdocs` skill on
10 Jun 2026, current-state, borrowing the proof-read `storage-provisioning-model` spec where it fit.

Confluence is the **source of truth**. To pull the full set into this directory as
frontmatter-bearing Markdown (round-trippable git copies), run the skill's adopter with a
Confluence API token configured:

```
scripts/adopt_existing.sh 981499912 docs/confluence/
```

`00-start-here.md` and `01-architecture-overview.md` are the authored sources (kept here);
the rest are authoritative in Confluence and adoptable via the command above.

## Pages (space TRIX, cloud `clustervision.atlassian.net`)

| Page | Confluence ID | URL |
|---|---|---|
| Luna Node Installer (landing / 00) | `981499912` | /spaces/TRIX/pages/981499912 |
| 01 — Architecture Overview | `981630983` | /spaces/TRIX/pages/981630983 |
| 02 — Module Map | `982286337` | /spaces/TRIX/pages/982286337 |
| 03 — Data Flows | `982253569` | /spaces/TRIX/pages/982253569 |
| 04 — Integrations | `982286357` | /spaces/TRIX/pages/982286357 |
| 05 — Dependencies | `982286378` | /spaces/TRIX/pages/982286378 |
| 06 — Configuration Reference | `982122501` | /spaces/TRIX/pages/982122501 |
| 07 — Testing & Quality | `981631003` | /spaces/TRIX/pages/981631003 |
| 08 — Operations & Runbook | `981532675` | /spaces/TRIX/pages/981532675 |
| 09 — Security Model | `982122522` | /spaces/TRIX/pages/982122522 |
| 10 — Gaps & Risks | `982319105` | /spaces/TRIX/pages/982319105 |
| 11 — Comparison & Prior Art | `981467140` | /spaces/TRIX/pages/981467140 |

## Source

Reconciled against `advanced-partitioner/docs/storage-provisioning-model.md` (the proof-read design
spec): §1→01/landing, §2/§5→06, §4→11, §6→03, §8→01/07, §9→10. Pages 00/02/04/05/08/09 extend
beyond the storage-model spec to the full installer + daemon data path.

## Diagrams

Eleven figures across five pages:

| Page | Figures |
|---|---|
| 01 | system context (Mermaid) · power-on→install→first-boot flow (Mermaid) · **Figure 4** colour-blocked install flow (`installer-flow-redhat.svg`) |
| 02 | component map — 95luna + lpart-* + daemon touch-points (Mermaid) |
| 03 | install sequence (Mermaid) · **live install TUI screenshot** (`tui.jpg`) |
| 06 | install_mode dispatch · discover/pin/manual lifecycle · data-fence apply-set (Mermaid) · the `auto` decision tree (`fig2-auto.svg`) |
| 11 | storage-representation positioning quadrant (Mermaid) |

Mermaid sources are fenced in each page's Markdown (rendered to SVG attachments by
`upload_confluence.py` + a `mmdc --no-sandbox` shim). The raw image attachments (the spec's own
SVGs and the TUI photo) live outside the Markdown — re-apply them after any page re-upload with
**`attach-figures.py`** (idempotent; needs Confluence creds).
