#!/usr/bin/env bash
# Renders each avatar-*.svg to a 512x512 PNG — the size Telegram wants for a
# bot photo (it downsamples to 160 and 40 itself; nothing here is tuned below
# 512, so re-render rather than hand-editing a PNG).
#
# Uses headless Chrome because it is the one SVG renderer present on every
# machine this repo gets built on. Override with CHROME=/path/to/chrome.
set -euo pipefail
cd "$(dirname "$0")"
CHROME="${CHROME:-/c/Program Files/Google/Chrome/Application/chrome.exe}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
for svg in avatar-*.svg; do
  png="${svg%.svg}.png"
  "$CHROME" --headless --disable-gpu --hide-scrollbars \
    --user-data-dir="$tmp/profile" --window-size=512,512 \
    --screenshot="$(pwd)/$png" "file://$(pwd -W 2>/dev/null || pwd)/$svg" 2>/dev/null
  echo "$png"
done

# The contact sheet, rendered from the PNGs above so it can never drift from
# them. --allow-file-access-from-files is needed only because preview.html
# builds its rows in script from local files.
tmp2="$(mktemp -d)"
trap 'rm -rf "$tmp" "$tmp2"' EXIT
"$CHROME" --headless --disable-gpu --hide-scrollbars --allow-file-access-from-files \
  --user-data-dir="$tmp2" --window-size=760,1000 \
  --screenshot="$(pwd)/preview.png" "file://$(pwd -W 2>/dev/null || pwd)/preview.html" 2>/dev/null
echo "preview.png"
