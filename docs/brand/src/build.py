"""Builds logo.html and svg/*.svg from marks.py. Run: python3 -I build.py (from this directory)."""
import base64
import itertools
import os
import re
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from marks import R3, VARIANTS  # noqa: E402

HERE = os.path.dirname(os.path.abspath(__file__))
REC = "trichter"
PAL = {"ink": "#12141A", "paper": "#FFFFFF", "night": "#0E1014", "mist": "#ECEEF2",
       "amber": "#E8930C", "amber_d": "#FFB020"}
TAGLINE = "Fan out to coding agents. Fan in to main."
_ids = itertools.count(1)


def uniq(s):
    """Give every mask id its own suffix so inlined copies do not collide."""
    n = next(_ids)
    return re.sub(r'(id="|url\(#)(m[a-z])', rf"\g<1>\g<2>{n}", s)


def svg(inner, cls="", label=""):
    aria = f'role="img" aria-label="{label}"' if label else 'aria-hidden="true"'
    return f'<svg viewBox="0 0 64 64" class="{cls}" {aria}>{uniq(inner)}</svg>'


def icon(key, inner):
    """App tile for favicon and avatar: night tile, light mark; the negative-space mark is a tile itself."""
    if key == "negativraum":
        return inner.replace("currentColor", PAL["ink"])
    return (f'<rect width="64" height="64" rx="15" fill="{PAL["night"]}"/>'
            f'<g transform="translate(32 32) scale(.8) translate(-32 -32)">{inner.replace("currentColor", PAL["mist"])}</g>')


def mono(inner):
    return inner.replace("var(--acc)", "currentColor")


def standalone(inner, color, acc):
    body = inner.replace("currentColor", color).replace("var(--acc)", acc)
    return f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="64" height="64">{body}</svg>\n'


def write_svgs():
    out = os.path.join(HERE, "svg")
    os.makedirs(out, exist_ok=True)
    files = {"runde3-fanin.svg": standalone(R3.replace("var(--a)", "#5b4bff").replace("var(--b)", "#ffb020"), "", "")}
    for key, _, inner, _ in VARIANTS:
        files[f"{key}.svg"] = standalone(inner, PAL["ink"], PAL["amber"])
        files[f"{key}-dark.svg"] = standalone(inner, PAL["mist"], PAL["amber_d"])
        files[f"{key}-mono.svg"] = standalone(mono(inner), "#000000", "#000000")
        files[f"{key}-icon.svg"] = standalone(icon(key, inner), PAL["ink"], PAL["amber_d"])
    for name, body in files.items():
        with open(os.path.join(out, name), "w") as fh:
            fh.write(body)
    return sorted(files)


def px16(name):
    """Real 16 px raster of an SVG file, shown enlarged with hard pixels."""
    png = subprocess.run(["rsvg-convert", "-w", "16", "-h", "16", os.path.join(HERE, "svg", name)],
                         capture_output=True, check=True).stdout
    return f'<img class="px4" alt="" src="data:image/png;base64,{base64.b64encode(png).decode()}">'


def wm(extra=""):
    return f'<span class="wm"{extra}>fanin</span>'


def r3(cls="", theme="pl"):
    return f'<span class="r3 {theme}">{svg(R3, cls, "fanin Runde 3")}</span>'


def compare(key, name, inner):
    cells = []
    for theme, tname in (("pl", "hell"), ("pd", "dunkel")):
        cells.append(f'<figure class="cmp {theme}"><div class="pair"><div>{r3("m96", theme)}<span class="cap">Runde 3</span></div>'
                     f'<div>{svg(inner, "m96", name)}<span class="cap">neu</span></div></div>'
                     f'<figcaption>{tname}</figcaption></figure>')
    return f'<div class="row2">{"".join(cells)}</div>'


def construction(inner):
    grid = "".join(f'<path d="M{i} 0V64M0 {i}H64"/>' for i in range(0, 65, 4))
    return (f'<svg viewBox="-2 -2 68 68" class="grid" aria-hidden="true"><g stroke="var(--line)" stroke-width=".25">{grid}</g>'
            f'<rect x="8" y="8" width="48" height="48" fill="none" stroke="var(--acc)" stroke-width=".4" stroke-dasharray="1.5 1"/>'
            f'<circle cx="32" cy="32" r="24" fill="none" stroke="var(--mut)" stroke-width=".3" stroke-dasharray="1 1"/>'
            f'<g opacity=".88">{uniq(inner)}</g></svg>')


def lockups(name, inner):
    out = []
    for theme in ("pl", "pd"):
        out.append(f'<div class="lk {theme}"><div class="hz">{svg(inner, "", name)}{wm()}</div></div>')
    for theme in ("pl", "pd"):
        out.append(f'<div class="lk {theme}"><div class="st">{svg(inner, "", name)}{wm()}</div></div>')
    return f'<div class="row4">{"".join(out)}</div>'


def monos(name, inner):
    m = mono(inner)
    cells = [("mo-l", m, "Monochrom"), ("mo-d", m, "Umgekehrt"), ("ac-l", inner, "Akzent auf Bernstein")]
    return '<div class="row3">' + "".join(
        f'<figure class="{c}"><div class="hz">{svg(s, "", name)}{wm()}</div><figcaption>{t}</figcaption></figure>'
        for c, s, t in cells) + "</div>"


def small(key, name, inner):
    ic = icon(key, inner)
    tabs = "".join(
        f'<div class="tab {t}"><span class="fav">{svg(ic, "s16", name)}</span><span>fanin · Runde 4</span>'
        f'<span class="x">×</span></div>' for t in ("tl", "td"))
    sizes = "".join(f'<span class="sz">{svg(ic, c, name)}<i>{n}</i></span>'
                    for c, n in (("s16", "16"), ("s32", "32"), ("s48", "48")))
    zoom = (f'<span class="sz">{px16(key + "-icon.svg")}<i>16 px, 4× gezoomt</i></span>'
            f'<span class="sz">{px16("runde3-fanin.svg")}<i>Runde 3, 16 px, 4×</i></span>')
    avatar = (f'<div class="av"><span class="sq">{svg(ic, "", name)}</span><span class="ci">{svg(ic, "", name)}</span>'
              f'<em>GitHub-Avatar 460 px, Quadrat und Kreisbeschnitt</em></div>')
    social = (f'<div class="soc"><div class="socin"><div class="soclk">{svg(inner, "", name)}{wm()}</div>'
              f'<p class="soctag">{TAGLINE}</p><p class="socurl">github.com/polandy/fanin</p></div>'
              f'<em>Social Preview 1280 × 640</em></div>')
    return (f'<div class="smalls">{tabs}<div class="sizes">{sizes}{zoom}</div></div>'
            f'<div class="share">{avatar}{social}</div>')


def variant(i, key, name, inner, idea):
    rec = key == REC
    badge = '<span class="badge">EMPFEHLUNG</span>' if rec else ""
    return f'''<article class="var{' rec' if rec else ''}" id="{key}">
<header><span class="num">{i:02d}</span><h2>{name}</h2>{badge}</header>
<div class="intro">{construction(inner)}<div><p>{idea}</p><p class="files">SVG: <code>svg/{key}.svg</code>, <code>-dark</code>, <code>-mono</code>, <code>-icon</code></p></div></div>
<h3>Vergleich mit Runde 3</h3>{compare(key, name, inner)}
<h3>Lockups: horizontal und gestapelt</h3>{lockups(name, inner)}
<h3>Monochrom, umgekehrt, Akzent</h3>{monos(name, inner)}
<h3>Favicon, Avatar, Social Preview</h3>{small(key, name, inner)}
</article>'''


def overview():
    cells = [f'<a class="ov" href="#runde3">{r3("m64", "pl")}{r3("m64", "pd")}<span>Runde 3</span></a>']
    for key, name, inner, _ in VARIANTS:
        cells.append(f'<a class="ov{" rec" if key == REC else ""}" href="#{key}"><span class="pl">{svg(inner, "m64", name)}</span>'
                     f'<span class="pd">{svg(inner, "m64", name)}</span><span>{name}</span></a>')
    return f'<nav class="ovgrid">{"".join(cells)}</nav>'


FONTS = [("Geist", 600, "-0.035em", "gewählt: geometrisch, aber mit offenen Formen; das f hat einen kurzen Bogen, der nicht über das a hängt"),
         ("Inter Tight", 600, "-0.02em", "neutral und sehr gut lesbar, aber die Schrift von fast jedem Dev-Tool"),
         ("IBM Plex Sans", 600, "-0.01em", "technisch und eigenständig, die Endstriche wirken neben den geometrischen Marken unruhig"),
         ("Manrope", 700, "-0.03em", "freundlich und rund, für ein Infrastruktur-Werkzeug etwas zu weich")]


def type_section():
    rows = "".join(f'<div class="fs{" pick" if i == 0 else ""}"><span class="wmx" style="font-family:\'{f}\';font-weight:{w};letter-spacing:{ls}">fanin</span>'
                   f'<div><b>{f} {w}</b><span>{d}</span></div></div>' for i, (f, w, ls, d) in enumerate(FONTS))
    return f'''<section class="basics"><h2>Grundlagen</h2>
<div class="bgrid">
<div class="bx"><h3>Raster</h3><p>Alle Marken auf einem 64er-Raster mit 4er-Teilung. Die Marke füllt das Feld 8 bis 56 (gestrichelt), Schutzzone ringsum 8 Einheiten = ⅛ der Markenhöhe. Strichstärke 6 (Merge-Graph 5) in jeder Variante gleich, runde Enden, keine Haarlinien: bei 16 px bleibt jeder Strich mindestens 1,2 px breit.</p></div>
<div class="bx"><h3>Farben</h3><div class="sw">
<span style="--c:{PAL["ink"]}"><b>Tinte</b>{PAL["ink"]}</span><span style="--c:{PAL["mist"]}"><b>Nebel</b>{PAL["mist"]}</span>
<span style="--c:{PAL["night"]}"><b>Nacht</b>{PAL["night"]}</span><span style="--c:{PAL["amber"]}"><b>Bernstein</b>{PAL["amber"]}</span><span style="--c:{PAL["amber_d"]}"><b>Bernstein dunkel</b>{PAL["amber_d"]}</span></div>
<p>Eine Akzentfarbe, nur für den Merge-Punkt bzw. die Linie nach <code>main</code>. Alles andere ist Tinte oder Nebel. Bernstein bleibt aus Runde 3, das Violett der Kachel fällt weg: ohne Kachelfarbe wirkt die Marke ruhiger und passt auf jeden Untergrund.</p></div>
</div>
<div class="bx"><h3>Wortmarke</h3><p>Klein geschrieben, <b>Geist SemiBold 600</b> (Google Fonts, SIL OFL), Laufweite −0,035 em. Geprüfte Paare: <code>fa</code> braucht keinen Extra-Ausgleich, weil der f-Bogen kurz ist; <code>an</code>, <code>ni</code>, <code>in</code> haben gleiche Innenweiten, die Wortmarke steht dadurch gleichmäßig. Der i-Punkt bleibt rund wie die Merge-Punkte der Marken. Für die endgültige Datei wird die Schrift in Pfade umgewandelt.</p>
<div class="fonts">{rows}</div></div>
</section>'''


CSS = '''
:root{--bg:#f5f6f8;--card:#fff;--text:#12141a;--mut:#5d6370;--line:#e2e4e9;--acc:#E8930C;--sans:'Geist',ui-sans-serif,system-ui,sans-serif;color-scheme:light dark}
@media (prefers-color-scheme:dark){:root{--bg:#0a0b0e;--card:#131519;--text:#eceef2;--mut:#9aa0ab;--line:#262931;--acc:#FFB020}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);font:15px/1.55 var(--sans);-webkit-font-smoothing:antialiased}
.wrap{max-width:1080px;margin:0 auto;padding:32px 16px 80px}
code{font:13px 'Geist Mono',ui-monospace,monospace;background:color-mix(in srgb,var(--text) 7%,transparent);padding:1px 5px;border-radius:4px}
h1{font-size:clamp(30px,6vw,46px);letter-spacing:-.03em;margin:0 0 4px;font-weight:600}
h2{font-size:22px;letter-spacing:-.02em;margin:0;font-weight:600}
h3{font-size:12px;letter-spacing:.08em;text-transform:uppercase;color:var(--mut);margin:28px 0 10px;font-weight:600}
.lead{color:var(--mut);max-width:680px;margin:8px 0 0}
.tagl{font-size:18px;margin:6px 0 0;font-weight:500}
section,article{background:var(--card);border:1px solid var(--line);border-radius:14px;padding:22px;margin-top:22px}
.pl{--ink:#12141A;--acc:#E8930C;--pbg:#fff;--a:#5b4bff;--b:#ffb020;color:var(--ink);background:var(--pbg)}
.pd{--ink:#ECEEF2;--acc:#FFB020;--pbg:#0E1014;--a:#9b90ff;--b:#ffc247;color:var(--ink);background:var(--pbg)}
.r3{display:inline-flex}
svg{display:block;flex:none}
.m96{width:96px;height:96px}.m64{width:56px;height:56px}
.s16{width:16px;height:16px}.s32{width:32px;height:32px}.s48{width:48px;height:48px}
.px4{width:64px;height:64px;image-rendering:pixelated}
.wm,.wmx{font-family:'Geist',var(--sans);font-weight:600;letter-spacing:-.035em;line-height:1;font-kerning:normal}
/* recommendation */
.verdict{border:1.5px solid var(--acc)}
.vhero{display:grid;grid-template-columns:1fr 1fr;gap:12px;margin:16px 0}
.vhero>div{border-radius:12px;display:flex;align-items:center;justify-content:center;padding:34px 16px;border:1px solid var(--line)}
.vhero .hz{font-size:clamp(34px,7vw,56px)}
.verdict ol{padding-left:20px;margin:8px 0 0}.verdict li{margin:4px 0}
/* overview */
.ovgrid{display:grid;grid-template-columns:repeat(auto-fill,minmax(120px,1fr));gap:10px;margin-top:16px}
.ov{display:grid;grid-template-columns:1fr 1fr;border:1px solid var(--line);border-radius:10px;overflow:hidden;text-decoration:none;color:var(--text)}
.ov>.pl,.ov>.pd,.ov>.r3{display:flex;justify-content:center;padding:12px 0}
.ov>span:last-child{grid-column:1/3;font-size:12.5px;padding:6px 8px;border-top:1px solid var(--line);font-weight:500}
.ov.rec{outline:2px solid var(--acc);outline-offset:-1px}
/* basics */
.bgrid{display:grid;grid-template-columns:1fr 1fr;gap:12px;margin-top:12px}
.bx{border:1px solid var(--line);border-radius:12px;padding:16px;margin-top:12px}.bgrid .bx{margin-top:0}
.bx h3{margin-top:0}.bx p{margin:8px 0 0}
.sw{display:flex;flex-wrap:wrap;gap:8px}
.sw span{display:flex;flex-direction:column;font:11.5px 'Geist Mono',monospace;color:var(--mut);min-width:78px}
.sw span::before{content:"";height:38px;border-radius:8px;background:var(--c);border:1px solid var(--line);margin-bottom:5px}
.sw b{font:600 12px var(--sans);color:var(--text)}
.fonts{display:grid;gap:8px;margin-top:12px}
.fs{display:flex;align-items:center;gap:18px;border:1px solid var(--line);border-radius:10px;padding:10px 14px}
.fs.pick{border-color:var(--acc)}
.fs .wmx{font-size:44px;min-width:150px}
.fs div{display:flex;flex-direction:column;font-size:13.5px}.fs div span{color:var(--mut)}
/* variant */
.var header{display:flex;align-items:center;gap:12px;flex-wrap:wrap}
.num{font:600 13px 'Geist Mono',monospace;color:var(--mut)}
.badge{font:600 11px var(--sans);letter-spacing:.08em;background:var(--acc);color:#12141a;padding:3px 9px;border-radius:99px}
.var.rec{border:1.5px solid var(--acc)}
.intro{display:grid;grid-template-columns:200px 1fr;gap:20px;align-items:center;margin-top:14px}
.intro p{margin:0 0 8px}.files{font-size:13px;color:var(--mut)}
.grid{width:200px;height:200px;color:var(--text);background:var(--bg);border-radius:10px}
.row2{display:grid;grid-template-columns:1fr 1fr;gap:12px}
.row3{display:grid;grid-template-columns:repeat(3,1fr);gap:12px}
.row4{display:grid;grid-template-columns:1fr 1fr;gap:12px}
figure{margin:0}
.cmp{border-radius:12px;border:1px solid var(--line);padding:18px 12px 10px}
.pair{display:flex;justify-content:space-around;gap:10px}
.pair>div{display:flex;flex-direction:column;align-items:center;gap:8px}
.cap,figcaption{font-size:12px;opacity:.65}
.cmp figcaption{text-align:center;margin-top:8px;border-top:1px solid color-mix(in srgb,currentColor 14%,transparent);padding-top:6px}
.lk{border-radius:12px;border:1px solid var(--line);display:flex;align-items:center;justify-content:center;padding:30px 12px;min-height:150px}
.hz{display:flex;align-items:center;gap:.32em;font-size:40px}
.hz svg{width:1.4em;height:1.4em}
.st{display:flex;flex-direction:column;align-items:center;gap:.22em;font-size:30px}
.st svg{width:2.1em;height:2.1em}
.row3 figure{border-radius:12px;border:1px solid var(--line);display:flex;flex-direction:column;align-items:center;justify-content:center;gap:12px;padding:24px 8px 12px}
.row3 .hz{font-size:28px}
.mo-l{background:#fff;color:#000;--acc:#000}
.mo-d{background:#000;color:#fff;--acc:#fff}
.ac-l{background:#FFB020;color:#12141A;--acc:#fff}
.smalls{display:grid;grid-template-columns:1fr 1fr;gap:12px}
.tab{display:flex;align-items:center;gap:8px;font:12.5px system-ui,sans-serif;padding:9px 12px;border-radius:10px 10px 0 0;border:1px solid var(--line);border-bottom:none}
.tab .x{margin-left:auto;opacity:.5}
.tl{background:#fff;color:#1f1f1f}.td{background:#35363a;color:#e8eaed}
.sizes{grid-column:1/3;display:flex;flex-wrap:wrap;align-items:flex-end;gap:22px;padding:16px;border:1px solid var(--line);border-radius:10px}
.sz{display:flex;flex-direction:column;align-items:center;gap:6px}.sz i{font:normal 11.5px 'Geist Mono',monospace;color:var(--mut)}
.share{display:grid;grid-template-columns:auto 1fr;gap:12px;margin-top:12px;align-items:start}
.share em{display:block;font:normal 12px var(--sans);color:var(--mut);margin-top:6px}
.av{display:grid;grid-template-columns:auto auto;gap:10px}.av em{grid-column:1/3}
.av svg{width:96px;height:96px}.av .ci svg{clip-path:circle(50%)}
.soc{min-width:0}
.socin{container-type:inline-size;aspect-ratio:2/1;border-radius:10px;background:#0E1014;color:#ECEEF2;--acc:#FFB020;
 display:flex;flex-direction:column;justify-content:center;padding:0 8cqw;
 background-image:linear-gradient(#ffffff0a 1px,transparent 1px),linear-gradient(90deg,#ffffff0a 1px,transparent 1px);background-size:4cqw 4cqw}
.soclk{display:flex;align-items:center;gap:2.6cqw}
.soclk svg{width:13cqw;height:13cqw}.soclk .wm{font-size:12cqw}
.soctag{font-size:3.6cqw;margin:3cqw 0 0;font-weight:500}
.socurl{font:3cqw 'Geist Mono',monospace;margin:1.4cqw 0 0;opacity:.55}
.foot{color:var(--mut);font-size:13px;margin-top:28px}
@media (max-width:720px){
 .vhero,.row2,.row4,.bgrid,.smalls,.share{grid-template-columns:1fr}
 .row3{grid-template-columns:1fr}
 .intro{grid-template-columns:1fr}.grid{width:180px;height:180px}
 .sizes{grid-column:auto}
 .fs{flex-direction:column;align-items:flex-start;gap:6px}
 .fs .wmx{font-size:36px;min-width:0}
 .hz{font-size:34px}
 .m96{width:72px;height:72px}
}
'''


def main():
    files = write_svgs()
    rk, rn, ri, _ = next(v for v in VARIANTS if v[0] == REC)
    verdict = f'''<section class="verdict" id="empfehlung"><h2>Empfehlung: {rn}</h2>
<div class="vhero"><div class="pl"><div class="hz">{svg(ri, "", rn)}{wm()}</div></div><div class="pd"><div class="hz">{svg(ri, "", rn)}{wm()}</div></div></div>
<ol>
<li><b>Hält bei 16 px.</b> Drei massive Flächen statt dünner Striche: das Favicon bleibt eine klare Form, wo Linien-Marken (Konvergenz, Graph) zu Strichelchen zerfallen.</li>
<li><b>Eigene Silhouette.</b> Linien, die sich treffen, haben viele Git- und CI-Werkzeuge; ein Fächer aus Keilen mit freigestelltem Punkt ist auch als Schattenriss wiedererkennbar.</li>
<li><b>Erzählt beide Hälften.</b> Von links gelesen läuft alles zusammen (<i>fan in</i>), als Ganzes ist es ein Fächer (<i>fan out</i>). Der Punkt ist der Merge nach <code>main</code>.</li>
<li><b>Monochrom ohne Verlust.</b> Der Punkt ist durch eine Fuge vom Rest getrennt; ohne Bernstein bleibt die Marke vollständig (Stempel, Terminal, Druck).</li>
<li><b>Sitzt neben der Wortmarke.</b> Breite und Gewicht der Keile entsprechen dem Grauwert von Geist SemiBold, das Lockup wirkt aus einem Guss.</li>
</ol>
<p>Zweite Wahl: <a href="#konvergenz">Konvergenz</a>, die direkteste Übersetzung des Musters, aber bei kleinen Größen und als Silhouette schwächer.</p></section>'''
    r3sec = f'''<section id="runde3"><h2>Ausgangspunkt: Runde 3</h2><p class="lead">Unverändert aus dem Namensblatt der Runde 3 übernommen. Was wir ändern: violette Kachel weg (die Marke steht frei), drei Input-Punkte weg (zu viele Elemente für 16 px), Kurven ersetzt durch Geometrie aus Geraden und Kreisen auf einem Raster, eine Strichstärke statt zwei, eine echte Schrift für die Wortmarke statt des fetten <i>in</i>.</p>
<div class="row2"><figure class="cmp pl"><div class="pair">{r3("m96", "pl")}</div><figcaption>hell</figcaption></figure><figure class="cmp pd"><div class="pair">{r3("m96", "pd")}</div><figcaption>dunkel</figcaption></figure></div></section>'''
    body = "".join(variant(i, *v) for i, v in enumerate(VARIANTS, 1))
    html = f'''<title>fanin Logo</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<link rel="preconnect" href="https://fonts.googleapis.com"><link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Geist:wght@400;500;600&family=Geist+Mono:wght@400;600&family=Inter+Tight:wght@600&family=IBM+Plex+Sans:wght@600&family=Manrope:wght@700&display=swap" rel="stylesheet">
<style>{CSS}</style>
<div class="wrap">
<p class="num">Runde 4 · Logo</p>
<h1><span class="wm">fanin</span></h1>
<p class="tagl">{TAGLINE}</p>
<p class="lead">Sieben Richtungen für die Marke von fanin (CLI <code>fanin</code>, Daemon <code>fanind</code>), jede mit Konstruktionsraster, Vergleich zu Runde 3 in hell und dunkel, Lockups, Monochrom- und Umkehrversion, Favicon, Avatar und Social Preview. Gemeinsame Regeln: ein Raster, eine Strichstärke, eine Akzentfarbe, eine Schrift.</p>
{overview()}
{verdict}
{type_section()}
{r3sec}
{body}
<p class="foot">Dateien: {len(files)} SVGs in <code>svg/</code> (Marke hell, dunkel, monochrom, App-Icon je Variante, dazu Runde 3). Erzeugt von <code>build.py</code> aus <code>marks.py</code>.</p>
</div>
'''
    with open(os.path.join(HERE, "logo.html"), "w") as fh:
        fh.write(html)
    print(f"logo.html {len(html)} bytes, {len(files)} svgs")


main()
