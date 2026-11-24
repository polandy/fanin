"""Regenerates every file of the fanin brand kit. Run src/regen.sh (needs nix) or:
python3 -I export.py   with fonttools + uharfbuzz installed and rsvg-convert + magick on PATH."""
import os
import subprocess
import sys

import uharfbuzz as hb
from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen
from fontTools.ttLib import TTFont

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
sys.path.insert(0, HERE)
from marks import wedges  # noqa: E402

INK, PAPER, NIGHT, MIST = "#12141A", "#FFFFFF", "#0E1014", "#ECEEF2"
AMBER, AMBER_D = "#E8930C", "#FFB020"
TAGLINE = "Fan out to coding agents. Fan in to main."
ART = (8, 8, 53.5, 56)  # mark keyline box: x0, y0, x1, y1 of the artwork in the 64 grid
# name -> (ink colour, accent colour); mono variants paint the merge dot in the ink colour
VARIANTS = {"light": (INK, AMBER), "dark": (MIST, AMBER_D), "mono-black": ("#000000", "#000000"),
            "mono-white": ("#FFFFFF", "#FFFFFF")}


def n(v):
    s = f"{v:.3f}".rstrip("0").rstrip(".")
    return "0" if s == "-0" else s


def mark_body(ink, acc):
    return wedges().replace("currentColor", ink).replace("var(--acc)", acc)


def write(path, text):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as fh:
        fh.write(text)


def render(svg_path, png_path, w, h):
    subprocess.run(["rsvg-convert", "-w", str(w), "-h", str(h), "-o", png_path, svg_path], check=True)


class Face:
    def __init__(self, fname):
        self.path = os.path.join(HERE, "fonts", fname)
        self.tt = TTFont(self.path)
        self.gs = self.tt.getGlyphSet()
        self.order = self.tt.getGlyphOrder()
        self.upem = self.tt["head"].unitsPerEm
        blob = hb.Blob.from_file_path(self.path)
        self.hbfont = hb.Font(hb.Face(blob))

    def outline(self, text, size, tracking=0.0, x=0.0, baseline=0.0):
        """Path data of text with kerning applied; returns (d, advance, ink_top)."""
        buf = hb.Buffer()
        buf.add_str(text)
        buf.guess_segment_properties()
        hb.shape(self.hbfont, buf, {"kern": True, "liga": True})
        k = size / self.upem
        d, pen_x = [], x
        for info, pos in zip(buf.glyph_infos, buf.glyph_positions):
            sp = SVGPathPen(self.gs, ntos=n)
            self.gs[self.order[info.codepoint]].draw(TransformPen(sp, (k, 0, 0, -k, pen_x + pos.x_offset * k, baseline)))
            d.append(sp.getCommands())
            pen_x += pos.x_advance * k + tracking * size
        return "".join(d), pen_x - x - tracking * size


SEMI, MED = Face("Geist-SemiBold.ttf"), Face("Geist-Medium.ttf")
TRACK = -0.035


def wordmark(size, x, baseline, fill):
    d, adv = SEMI.outline("fanin", size, TRACK, x, baseline)
    return f'<path d="{d}" fill="{fill}"/>', adv


def mark_group(ink, acc, x, y, scale):
    return f'<g transform="translate({n(x)} {n(y)}) scale({n(scale)})">{mark_body(ink, acc)}</g>'


def svg_doc(w, h, body, vb=None, bg=None):
    vb = vb or f"0 0 {n(w)} {n(h)}"
    rect = f'<rect width="100%" height="100%" fill="{bg}"/>' if bg else ""
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="{vb}" width="{n(w)}" height="{n(h)}">{rect}{body}</svg>\n')


# ink box of "fanin" at size 1: ascender top of f/i dot to baseline (no descenders)
ASC = 0.72


def lockup_h(ink, acc, fs=64.0):
    """Horizontal: art box height 1.05 fs, wordmark ink centred on the art's vertical centre."""
    s = 1.05 * fs / (ART[3] - ART[1])
    art_w, art_h = (ART[2] - ART[0]) * s, (ART[3] - ART[1]) * s
    gap = 0.22 * fs
    base = art_h / 2 + ASC * fs / 2
    wm, adv = wordmark(fs, art_w + gap, base, ink)
    body = mark_group(ink, acc, -ART[0] * s, -ART[1] * s, s) + wm
    return svg_doc(art_w + gap + adv, art_h, body)


def lockup_s(ink, acc, fs=64.0):
    """Stacked: art box 1.9 fs high, wordmark centred below, gap 0.28 fs."""
    s = 1.9 * fs / (ART[3] - ART[1])
    art_w, art_h = (ART[2] - ART[0]) * s, (ART[3] - ART[1]) * s
    gap = 0.28 * fs
    _, adv = SEMI.outline("fanin", fs, TRACK)
    w = max(adv, art_w)
    wm, _ = wordmark(fs, (w - adv) / 2, art_h + gap + ASC * fs, ink)
    body = mark_group(ink, acc, (w - art_w) / 2 - ART[0] * s, -ART[1] * s, s) + wm
    return svg_doc(w, art_h + gap + ASC * fs + 0.02 * fs, body)


def mark_svg(ink, acc):
    return svg_doc(64, 64, mark_body(ink, acc))


def tile(rx, scale=0.8, bg=NIGHT):
    c = 32 - 32 * scale
    return (f'<rect width="64" height="64" rx="{rx}" fill="{bg}"/>'
            f'<g transform="translate({n(c)} {n(c)}) scale({n(scale)})">{mark_body(MIST, AMBER_D)}</g>')


def main():
    img = lambda *p: os.path.join(ROOT, *p)
    # logo/: mark, horizontal and stacked lockups, four colourways, SVG + PNG 1x/2x
    for name, (ink, acc) in VARIANTS.items():
        for kind, svgtext in (("mark", mark_svg(ink, acc)), ("lockup-horizontal", lockup_h(ink, acc)),
                                      ("lockup-stacked", lockup_s(ink, acc))):
            stem = img("logo", f"fanin-{kind}-{name}")
            write(stem + ".svg", svgtext)
            vb = [float(v) for v in svgtext.split('viewBox="')[1].split('"')[0].split()]
            w1 = 256 if kind == "mark" else round(vb[2] * 2) if kind == "lockup-horizontal" else round(vb[2] * 1.5)
            h1 = round(w1 * vb[3] / vb[2])
            render(stem + ".svg", stem + ".png", w1, h1)
            render(stem + ".svg", stem + "@2x.png", w1 * 2, h1 * 2)
    # icon/: rounded app icon, full-bleed variant for apple-touch, small sizes with a larger mark
    write(img("icon", "fanin-icon.svg"), svg_doc(64, 64, tile(15)))
    write(img("icon", "fanin-icon-small.svg"), svg_doc(64, 64, tile(15, 0.94)))
    write(img("icon", "fanin-icon-square.svg"), svg_doc(64, 64, tile(0)))
    for px in (16, 32, 48, 64, 128, 256, 512, 1024):
        render(img("icon", "fanin-icon-small.svg" if px <= 32 else "fanin-icon.svg"), img("icon", f"fanin-icon-{px}.png"), px, px)
    render(img("icon", "fanin-icon-square.svg"), img("icon", "apple-touch-icon.png"), 180, 180)
    render(img("icon", "fanin-icon-small.svg"), img("icon", "favicon-48.png"), 48, 48)
    subprocess.run(["magick", img("icon", "fanin-icon-16.png"), img("icon", "fanin-icon-32.png"), img("icon", "favicon-48.png"),
                    img("icon", "favicon.ico")], check=True)
    os.remove(img("icon", "favicon-48.png"))
    write(img("icon", "favicon.svg"), svg_doc(64, 64, tile(15, 0.94)))
    # social/: avatar (full-bleed, survives circle crop) and 1280x640 preview
    write(img("social", "fanin-org-avatar.svg"), svg_doc(500, 500, tile(0, 0.8), vb="0 0 64 64"))
    render(img("social", "fanin-org-avatar.svg"), img("social", "fanin-org-avatar.png"), 500, 500)
    fs = 150.0
    s = 1.05 * fs / (ART[3] - ART[1])
    art_w, art_h = (ART[2] - ART[0]) * s, (ART[3] - ART[1]) * s
    gap = 0.32 * fs
    _, adv = SEMI.outline("fanin", fs, TRACK)
    tfs = 40.0
    _, tadv = MED.outline(TAGLINE, tfs, 0.0)
    lw = art_w + gap + adv
    x0, y_top = (1280 - lw) / 2, 200.0
    base = y_top + art_h / 2 + ASC * fs / 2
    wm, _ = wordmark(fs, x0 + art_w + gap, base, MIST)
    tag, tadv = MED.outline(TAGLINE, tfs, 0.0, (1280 - tadv) / 2, y_top + art_h + 88)
    body = (mark_group(MIST, AMBER_D, x0 - ART[0] * s, y_top - ART[1] * s, s) + wm + f'<path d="{tag}" fill="{MIST}" fill-opacity=".78"/>')
    write(img("social", "fanin-social-preview.svg"), svg_doc(1280, 640, body, bg=NIGHT))
    render(img("social", "fanin-social-preview.svg"), img("social", "fanin-social-preview.png"), 1280, 640)


if __name__ == "__main__":
    main()
