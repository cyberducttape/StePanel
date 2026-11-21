#!/usr/bin/env bash
# Builds docs/assets/screenshots/tour.gif, the README tour, from the real-UI
# screenshots that scripts/capture-screenshots.sh produces. Requires ffmpeg.
set -Eeuo pipefail
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
shots=$repo/docs/assets/screenshots
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
index=0
for name in operator-overview sites site-workspace site-backups activity customer-overview; do
  cp "$shots/$name.png" "$work/$(printf '%02d' "$index").png"
  index=$((index + 1))
done
ffmpeg -loglevel error -y -framerate 1/2.5 -i "$work/%02d.png" \
  -vf "fps=8,scale=960:-1:flags=lanczos,split[s0][s1];[s0]palettegen=max_colors=128:stats_mode=diff[p];[s1][p]paletteuse=dither=bayer:bayer_scale=4" \
  -loop 0 "$shots/tour.gif"
echo "wrote $shots/tour.gif"
