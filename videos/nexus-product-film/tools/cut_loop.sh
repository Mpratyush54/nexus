#!/usr/bin/env bash
# Usage: cut_loop.sh <full.mp4> <outdir> "s1:d1 s2:d2 ..."
# Cuts segments from the full film, joins them with short crossfades, loops back
# to the first frame, drops audio, and writes H.264 (faststart) + VP9 WebM.
set -euo pipefail
IN="$1"; OUT="$2"; SEGS="$3"; XF=0.35
mkdir -p "$OUT"
args=(); filt=""; i=0
for seg in $SEGS; do
  s="${seg%%:*}"; d="${seg##*:}"
  args+=(-ss "$s" -t "$d" -i "$IN")
  filt+="[$i:v]setpts=PTS-STARTPTS,fps=30,format=yuv420p[v$i];"
  durs[$i]="$d"; i=$((i+1))
done
n=$i; prev="v0"; acc="${durs[0]}"
for ((k=1;k<n;k++)); do
  off=$(python -c "print(round($acc-$XF,3))")
  filt+="[$prev][v$k]xfade=transition=fade:duration=$XF:offset=$off[x$k];"
  prev="x$k"; acc=$(python -c "print(round($acc+${durs[$k]}-$XF,3))")
done
# Fade the tail to black and the head from black so the loop seam is soft.
filt+="[$prev]fade=t=in:st=0:d=0.3,fade=t=out:st=$(python -c "print(round($acc-0.35,3))"):d=0.35[out]"
ffmpeg -v error -y "${args[@]}" -filter_complex "$filt" -map "[out]" -an -c:v libx264 -preset slow -crf 26 -maxrate 3M -bufsize 6M -pix_fmt yuv420p -movflags +faststart "$OUT/nexus-film.mp4"
ffmpeg -v error -y -i "$OUT/nexus-film.mp4" -an -c:v libvpx-vp9 -b:v 0 -crf 40 -row-mt 1 -deadline good -cpu-used 4 "$OUT/nexus-film.webm"
echo "loop length: $acc s"
