"""fanin mark geometry. Grid 64x64, keyline 8..56, ink = currentColor, accent = var(--acc)."""
import math

INK = "currentColor"
ACC = "var(--acc)"

# Round 3, copied verbatim from the round-3 naming sheet (card fanin, abstract geometry).
R3 = '''<rect x="2" y="2" width="60" height="60" rx="16" fill="var(--a)"/>
<g fill="none" stroke="#fff" stroke-width="5" stroke-linecap="round"><path d="M14 16C26 16 27 32 38 32"/><path d="M14 48C26 48 27 32 38 32"/><path d="M14 32H38"/></g>
<path d="M38 32H46" stroke="#fff" stroke-width="7" stroke-linecap="round"/><circle cx="49" cy="32" r="6.5" fill="var(--b)"/>
<circle cx="14" cy="16" r="3.5" fill="#fff"/><circle cx="14" cy="32" r="3.5" fill="#fff"/><circle cx="14" cy="48" r="3.5" fill="#fff"/>'''


def f(v):
    s = f"{v:.2f}".rstrip("0").rstrip(".")
    return "0" if s == "-0" else s


def converge():
    # three straight strokes meet at (34,32), the merged line leaves in the accent colour
    return (f'<g fill="none" stroke-width="6" stroke-linecap="round" stroke-linejoin="round">'
            f'<path stroke="{INK}" d="M11 13L34 32M11 51L34 32M11 32H34"/>'
            f'<path stroke="{ACC}" d="M34 32H53"/></g>')


def graph():
    # subway geometry: horizontal, then 45 degrees into one merge node
    return (f'<g fill="none" stroke="{INK}" stroke-width="5" stroke-linejoin="round">'
            f'<circle cx="13.5" cy="13" r="5"/><circle cx="13.5" cy="32" r="5"/><circle cx="13.5" cy="51" r="5"/>'
            f'<path d="M18.5 13H23L42 32M18.5 51H23L42 32M18.5 32H42"/></g>'
            f'<circle cx="47" cy="32" r="8" fill="{ACC}"/>')


def wedges():
    # three solid blades share one apex hidden under the merge dot; gaps taper to zero
    ax, ay = 46, 32
    blades = [(8, 21), (25.5, 38.5), (43, 56)]
    d = "".join(f"M8 {f(a)}L{ax} {ay}L8 {f(b)}Z" for a, b in blades)
    return (f'<mask id="mw"><rect width="64" height="64" fill="#fff"/><circle cx="{ax}" cy="{ay}" r="11.5" fill="#000"/></mask>'
            f'<path d="{d}" fill="{INK}" mask="url(#mw)"/>'
            f'<circle cx="{ax}" cy="{ay}" r="7.5" fill="{ACC}"/>')


def monogram_f():
    # lowercase f: stem and hook; the crossbar's left half is two strokes fanning into the stem
    return (f'<g fill="none" stroke-width="6" stroke-linecap="round" stroke-linejoin="round">'
            f'<path stroke="{INK}" d="M31 56V22A12 12 0 0 1 43 10H48"/>'
            f'<path stroke="{INK}" d="M14 22L31 32M14 42L31 32"/>'
            f'<path stroke="{ACC}" d="M31 32H46"/></g>')


def ligature_fi():
    # fi ligature: the f hook runs into the i dot, which is the merge point
    return (f'<g fill="none" stroke="{INK}" stroke-width="6" stroke-linecap="round" stroke-linejoin="round">'
            f'<path d="M20 56V23A11 11 0 0 1 31 12H38"/>'
            f'<path d="M11 30H31M47 30V56"/></g>'
            f'<circle cx="47" cy="12" r="5.5" fill="{ACC}"/>')


def tile():
    # negative space: the convergence is cut out of a solid tile, the merged line breaks out of it
    return (f'<mask id="mt"><rect width="64" height="64" fill="#fff"/>'
            f'<g fill="none" stroke="#000" stroke-width="6" stroke-linecap="round">'
            f'<path d="M17 19L36 32M17 45L36 32M17 32H36"/></g>'
            f'<rect x="36" y="26" width="28" height="12" fill="#000"/></mask>'
            f'<rect x="4" y="4" width="52" height="56" rx="13" fill="{INK}" mask="url(#mt)"/>'
            f'<path d="M36 32H61" stroke="{ACC}" stroke-width="6" stroke-linecap="round"/>')


def sector(cx, cy, r0, r1, a0, a1):
    p = lambda r, a: (cx + r * math.cos(math.radians(a)), cy + r * math.sin(math.radians(a)))
    (x0, y0), (x1, y1), (x2, y2), (x3, y3) = p(r1, a0), p(r1, a1), p(r0, a1), p(r0, a0)
    return (f"M{f(x0)} {f(y0)}A{r1} {r1} 0 0 1 {f(x1)} {f(y1)}"
            f"L{f(x2)} {f(y2)}A{r0} {r0} 0 0 0 {f(x3)} {f(y3)}Z")


def fan():
    # folding fan: three blades around one pivot; the pivot is the merge
    cx, cy = 47, 32
    d = "".join(sector(cx, cy, 12, 37, a, a + 22) for a in (141, 169, 197))
    return f'<path d="{d}" fill="{INK}"/><circle cx="{cx}" cy="{cy}" r="6.5" fill="{ACC}"/>'


VARIANTS = [
    ("konvergenz", "Konvergenz", converge(),
     "Drei gerade Striche treffen sich in einem Punkt und gehen als einer weiter. Die Linie danach ist <code>main</code> und trägt die Akzentfarbe."),
    ("graph", "Merge-Graph", graph(),
     "Commit-Graph in U-Bahn-Geometrie: waagerecht, dann 45°, ein gemeinsamer Merge-Knoten. Spricht die Sprache von Git-Werkzeugen."),
    ("trichter", "Trichter", wedges(),
     "Drei massive Keile mit gemeinsamer Spitze, die Fugen laufen auf null zu. Der Merge-Punkt sitzt freigestellt auf der Spitze."),
    ("monogramm-f", "f-Monogramm", monogram_f(),
     "Ein kleines f, dessen Querstrich links aus zwei Strichen zusammenläuft. Buchstabe und Bewegung in einem Zeichen."),
    ("ligatur-fi", "fi-Ligatur", ligature_fi(),
     "Die klassische fi-Ligatur: der Bogen des f läuft auf den i-Punkt zu, der i-Punkt ist der Merge. Liest sich als Kürzel von <i>fanin</i>."),
    ("negativraum", "Negativraum", tile(),
     "Die Konvergenz ist aus einer vollen Fläche ausgestanzt, nur die Hauptlinie bricht in Akzentfarbe aus der Kachel aus."),
    ("faecher", "Fächer", fan(),
     "Ein Faltfächer aus drei Blättern um einen Drehpunkt. Wörtlich <i>fan</i>, der Drehpunkt ist der Merge."),
]
