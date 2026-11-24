#!/usr/bin/env bash
# Regenerates all brand files from src/. Needs nix; or install fonttools, uharfbuzz, librsvg and imagemagick yourself.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec nix shell --impure --expr 'with import <nixpkgs> {}; [ (python3.withPackages (p: [p.fonttools p.uharfbuzz])) librsvg imagemagick ]' \
  -c python3 -I "$here/export.py"
