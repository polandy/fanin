# fanin brand kit

Logo direction **Trichter** (funnel): three solid wedges share one apex, the gaps between them taper to zero, and the merge point sits cut free on the apex. Fan out, fan in.

## Palette

| Name | Hex | Use |
| --- | --- | --- |
| Ink | `#12141A` | mark and wordmark on light backgrounds |
| Amber | `#E8930C` | merge dot on light backgrounds |
| Mist | `#ECEEF2` | mark and wordmark on dark backgrounds |
| Amber (dark) | `#FFB020` | merge dot on dark backgrounds |
| Night | `#0E1014` | app icon tile, dark backgrounds |
| Paper | `#FFFFFF` | light backgrounds |

Mono versions use one colour for the wedges and the dot (black `#000000` or white `#FFFFFF`).

## Typeface

Wordmark: **Geist SemiBold 600**, lowercase `fanin`, letter-spacing -0.035 em, kerning on. Taglines and captions: Geist Medium 500. Geist is licensed under the SIL OFL 1.1 (`src/fonts/OFL.txt`). In the SVG files the wordmark and tagline are converted to outlined paths, so no font is needed to display them.

## Clear space

Keep free space of at least **1/4 of the mark height** on all sides of the mark or a lockup (measured on the wedge artwork, not on the 64-unit canvas). Do not place text or other graphics inside it.

## Minimum size

- Mark alone: 16 px (use `icon/fanin-icon-small.svg`, whose mark fills more of the tile), 8 mm in print.
- Horizontal lockup: 96 px wide, 25 mm in print.
- Stacked lockup: 64 px wide, 18 mm in print.

## Usage

- Light backgrounds: `light`. Dark backgrounds: `dark`. One-colour printing, stamps, embossing: `mono-black` / `mono-white`.
- Do not recolour, stretch, rotate, outline or add effects. Do not retype the wordmark in another font.
- SVG files are transparent. The `dark` and `mono-white` PNGs are transparent too, so preview them on a dark background.

## Files

`logo/` (SVG, and PNG at 1x and `@2x`, colourways `light`, `dark`, `mono-black`, `mono-white`)

- `fanin-mark-<colourway>`: the mark alone (64 x 64 grid, 256 px at 1x)
- `fanin-lockup-horizontal-<colourway>`: mark and wordmark side by side (451 x 134 px at 1x for the light/dark/mono sets)
- `fanin-lockup-stacked-<colourway>`: mark above the wordmark (221 x 280 px at 1x)

`icon/`

- `fanin-icon.svg`: rounded app icon, Night tile with Mist mark and dark amber dot
- `fanin-icon-small.svg`: same with a larger mark, for 32 px and below
- `fanin-icon-square.svg`: full-bleed square without rounded corners
- `fanin-icon-{16,32,48,64,128,256,512,1024}.png`: 16 and 32 use the small variant
- `favicon.ico`: 16, 32 and 48 px; `favicon.svg`: for modern browsers
- `apple-touch-icon.png`: 180 px, full-bleed (iOS rounds the corners itself)

`social/`

- `fanin-social-preview.svg` / `.png`: GitHub social preview, 1280 x 640, mark, wordmark and the tagline "Fan out to coding agents. Fan in to main."
- `fanin-org-avatar.svg` / `.png`: organisation avatar, 500 x 500, full-bleed so it survives a circle crop

`src/`

- `marks.py`: mark geometry (the `wedges()` function is the chosen mark; the other functions are the discarded round-4 candidates)
- `build.py`: generator of the original exploration sheet (all candidates, German, writes `logo.html` and `svg/`; kept for reference)
- `export.py`: generates everything above; `regen.sh` runs it through `nix shell` (fonttools, uharfbuzz, librsvg, imagemagick)
- `fonts/`: Geist SemiBold and Medium plus the OFL text

Regenerate with `src/regen.sh`.

## License

Same as the fanin repo; see `LICENSE.md`.
