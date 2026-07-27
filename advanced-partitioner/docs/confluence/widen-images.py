#!/usr/bin/env python3
"""Force every embedded image on the Luna Node Installer pages to a readable display width.

Mermaid SVGs (and small attachments) render at their tiny native size unless the <ac:image>
macro carries an explicit width. This sets ac:width on every image so it fills the content
column. Idempotent; re-run after any page re-upload. Needs Confluence creds (.env.confluence).
"""
import os, re, sys
sys.path.insert(0, "/root/git/confluence-devdocs/scripts")
from confluence_auth import get_confluence_client

WIDTH = "1000"
PAGES = ["981499912","981630983","982286337","982253569","982286357","982286378",
         "982122501","981631003","981532675","982122522","982319105","981467140"]

def set_full_width(c, pid):
    """Set the page to full-width appearance (a content property — persists across body updates)."""
    for key in ("content-appearance-published", "content-appearance-draft"):
        try:
            existing = None
            try:
                existing = c.get_page_property(pid, key)
            except Exception:
                pass
            if existing and existing.get("value") == "full-width":
                continue
            if existing:
                c.update_page_property(pid, {"key": key, "value": "full-width",
                                             "version": {"number": existing["version"]["number"] + 1}})
            else:
                c.set_page_property(pid, {"key": key, "value": "full-width"})
        except Exception as e:
            print(f"  {pid}/{key}: {repr(e)[:100]}")


def main():
    c = get_confluence_client()
    for pid in PAGES:
        set_full_width(c, pid)
        p = c.get_page_by_id(pid, expand="body.storage,version")
        body = p["body"]["storage"]["value"]
        # add ac:width to any <ac:image without one
        new = re.sub(r'<ac:image(?![^>]*ac:width)', f'<ac:image ac:width="{WIDTH}"', body)
        # normalise any existing width to WIDTH (in case a smaller one was set before)
        new = re.sub(r'ac:width="\d+"', f'ac:width="{WIDTH}"', new)
        n = new.count('ac:width=')
        if new == body:
            print(f"{pid}: no images / already {WIDTH} ({n})"); continue
        c.update_page(page_id=pid, title=p["title"], body=new, type="page",
                      representation="storage", minor_edit=True,
                      version_comment=f"set image display width to {WIDTH}px")
        print(f"{pid}: widened {n} image(s) -> v{p['version']['number']+1}")

if __name__ == "__main__":
    main()
