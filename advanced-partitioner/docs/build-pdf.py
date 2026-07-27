#!/usr/bin/env python3
import re, pathlib, markdown

DOCDIR = pathlib.Path(__file__).resolve().parent
md_path = DOCDIR / "storage-provisioning-model.md"
html_path = DOCDIR / "storage-provisioning-model.html"

text = md_path.read_text()

md = markdown.Markdown(extensions=["extra", "tables", "fenced_code", "sane_lists",
                                   "toc", "codehilite"],
                       extension_configs={"codehilite": {"guess_lang": False,
                                                          "noclasses": True,
                                                          "pygments_style": "friendly"}})
body = md.convert(text)

# Inline the SVG diagrams: replace <img ... src="diagrams/X.svg" ...> with a <figure>.
def inline_svg(m):
    tag = m.group(0)
    src = re.search(r'src="([^"]+)"', tag).group(1)
    alt = re.search(r'alt="([^"]*)"', tag)
    alt = alt.group(1) if alt else ""
    svg_file = DOCDIR / src
    svg = svg_file.read_text()
    cap = f'<figcaption>{alt}</figcaption>' if alt.strip() else ''
    return f'<figure>{svg}{cap}</figure>'

body = re.sub(r'<img[^>]*src="diagrams/[^"]+\.svg"[^>]*/?>', inline_svg, body)

# Wrap raster images (png/jpg) in a <figure> too, so they centre, get the alt as
# a caption, and pick up the max-width cap below (else they overflow the A4 page).
def wrap_raster(m):
    tag = m.group(0)
    alt = re.search(r'alt="([^"]*)"', tag)
    alt = alt.group(1) if alt else ""
    cap = f'<figcaption>{alt}</figcaption>' if alt.strip() else ''
    return f'<figure>{tag}{cap}</figure>'

body = re.sub(r'<img[^>]*src="diagrams/[^"]+\.(?:png|jpe?g)"[^>]*/?>', wrap_raster, body)

CSS = """
@page { size: A4; margin: 18mm 16mm 20mm 16mm;
        @bottom-center { content: "TrinityX / Luna Node Provisioning Model"; font-size: 8pt; color:#9ca3af; }
        @bottom-right  { content: counter(page) " / " counter(pages); font-size: 8pt; color:#9ca3af; } }
* { box-sizing: border-box; }
body { font-family: "Helvetica Neue", Helvetica, Arial, sans-serif; font-size: 10.5pt;
       line-height: 1.5; color: #1f2937; }
h1 { font-size: 22pt; color: #0f172a; margin: 0 0 2pt; }
h2 { font-size: 15pt; color: #0f172a; margin-top: 20pt; padding-bottom: 3pt;
     border-bottom: 2px solid #cbd5e1; }
h3 { font-size: 12.5pt; color: #1e293b; margin-top: 14pt; }
h2, h3 { page-break-after: avoid; break-after: avoid-page; }
p, li { orphans: 2; widows: 2; }
code { font-family: "SFMono-Regular", Consolas, monospace; font-size: 9pt;
       background: #f1f5f9; padding: 1px 4px; border-radius: 3px; }
pre { background: #f8fafc; border: 1px solid #e2e8f0; border-left: 3px solid #64748b;
      border-radius: 4px; padding: 8px 10px; font-size: 8.6pt; line-height: 1.4;
      overflow-x: auto; white-space: pre-wrap; word-wrap: break-word;
      page-break-inside: avoid; }
pre code { background: none; padding: 0; font-size: inherit; }
blockquote { margin: 10pt 0; padding: 6pt 12pt; background: #eff6ff;
             border-left: 3px solid #3b82f6; color: #1e3a8a; font-size: 9.8pt; }
table { border-collapse: collapse; width: 100%; margin: 8pt 0; font-size: 8.8pt;
        page-break-inside: avoid; }
th, td { border: 1px solid #cbd5e1; padding: 4px 7px; text-align: left; vertical-align: top; }
th { background: #1e293b; color: #fff; font-weight: 600; }
th code { background: transparent; color: #e5e7eb; padding: 0; }
tr:nth-child(even) td { background: #f8fafc; }
figure { margin: 14pt 0; text-align: center; page-break-inside: avoid; }
figure svg, figure img { max-width: 100%; height: auto; }
figcaption { font-size: 8.6pt; color: #6b7280; margin-top: 4pt; font-style: italic; }
hr { border: none; border-top: 1px solid #e5e7eb; margin: 16pt 0; }
a { color: #2563eb; text-decoration: none; }
ul, ol { margin: 6pt 0; padding-left: 20pt; }
li { margin: 2pt 0; }
strong { color: #0f172a; }
"""

html = f"""<!DOCTYPE html><html lang="en"><head><meta charset="utf-8">
<title>TrinityX / Luna — Node Storage &amp; Provisioning Model</title>
<style>{CSS}</style></head><body>{body}</body></html>"""

html_path.write_text(html)
print("wrote", html_path, len(html), "bytes")
