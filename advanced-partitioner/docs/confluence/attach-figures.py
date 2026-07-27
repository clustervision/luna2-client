#!/usr/bin/env python3
"""Attach the design-spec's own rendered figures (and the TUI photo) to the published
Luna Node Installer pages and embed them, after a `upload_confluence.py` run.

These are raw image attachments (not Mermaid), so they live outside the page Markdown —
re-run this after any script re-upload of the affected pages. Idempotent: skips a figure
already embedded. Needs Confluence creds (see the skill's .env.confluence).

    python3 attach-figures.py            # from the confluence-devdocs venv, with scripts/ importable
"""
import os, sys
SKILL = "/root/git/confluence-devdocs"
sys.path.insert(0, os.path.join(SKILL, "scripts"))
from confluence_auth import get_confluence_client

DIAG = "/root/git/gitlab-luna2-client/advanced-partitioner/docs/diagrams"

# (page_id, file, filename, mime, caption, insert-before-heading-substring)
FIGURES = [
    ("981630983", f"{DIAG}/installer-flow-redhat.svg", "installer-flow-redhat.svg", "image/svg+xml",
     "Figure 4 — the full power-on to first-boot install flow, every install.sh step colour-coded by who runs it.",
     "System boundaries"),
    ("982122501", f"{DIAG}/fig2-auto.svg", "fig2-auto.svg", "image/svg+xml",
     "The auto decision tree: a declared OS layout that cannot be satisfied halts (never silently falls back to RAM).",
     "Example"),
    ("982253569", f"{DIAG}/tui.jpg", "tui.jpg", "image/jpeg",
     "The live install TUI on a compute node during an OS image install.",
     "Getting the image onto the node"),
]

def main():
    c = get_confluence_client()
    for pid, path, name, mime, caption, marker in FIGURES:
        if not os.path.exists(path):
            print("skip (missing file):", name); continue
        try:
            c.attach_file(path, name=name, content_type=mime, page_id=pid, comment="design-spec figure")
        except Exception as e:
            print(f"attach {name}: {repr(e)[:120]}")
        page = c.get_page_by_id(pid, expand="body.storage,version")
        body = page["body"]["storage"]["value"]
        if f'ri:filename="{name}"' in body:
            print(f"{name} already embedded on {pid}"); continue
        fig = (f'<ac:image ac:align="center"><ri:attachment ri:filename="{name}"/></ac:image>'
               f'<p><em>{caption}</em></p>')
        idx = body.find(f"<h2>{marker}")
        if idx == -1:
            idx = body.find(marker)
        if idx == -1:
            idx = body.rfind("<hr")
        body = body[:idx] + fig + body[idx:]
        c.update_page(page_id=pid, title=page["title"], body=body, type="page",
                      representation="storage", minor_edit=False,
                      version_comment=f"embed {name}")
        print(f"embedded {name} on {pid} (v{page['version']['number']+1})")

if __name__ == "__main__":
    main()
