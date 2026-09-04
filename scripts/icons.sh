#!/usr/bin/env bash
# Rebuild every favicon from web/public/icon.svg, which is the one source.
#
# Google will not read a data: URI favicon, which is what this site shipped
# with for months: browser tabs showed the rook, search results showed the
# generic globe. Google also wants a real file at a stable URL, ideally sized
# to a multiple of 48. Hence the files below rather than an inline SVG.
#
# Needs librsvg: brew install librsvg
set -euo pipefail
cd "$(dirname "$0")/.."
SRC=web/public/icon.svg
command -v rsvg-convert >/dev/null || { echo "need rsvg-convert (brew install librsvg)"; exit 1; }

for s in 16 32 48 96 180 192 512; do
  rsvg-convert -w $s -h $s "$SRC" -o "web/public/icon-${s}.png"
done

# Google's crawler and older browsers still ask for /favicon.ico by name.
python3 scripts/makeico.py web/public/icon-16.png web/public/icon-32.png web/public/icon-48.png web/public/favicon.ico

# Only the sizes actually referenced ship; the rest were scaffolding.
mv web/public/icon-180.png web/public/apple-touch-icon.png
rm -f web/public/icon-16.png web/public/icon-32.png web/public/icon-48.png
echo "icons rebuilt:"
ls -la web/public/*.png web/public/*.ico web/public/icon.svg
