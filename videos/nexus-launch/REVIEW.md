# Product-led revision review — 24 October 2026

The first draft was rejected by the user as not matching the requested Apple
reference. Technical passes on that draft were not evidence of creative fit.
The approved scattered-to-shared story remains; all six scenes were rebuilt.

## Creative decisions

Studied actual official MacBook Pro and iPhone Air film frames, not a template
labelled “Apple style”. REFERENCE-STUDY.md separates observed scale/continuity
from our inferred software translation. No Apple footage, audio, typeface or
logo is included in delivery assets. The alternative agent-relay/team-first
storylines were not built.

The product now leads: conversation detail, assembled interface, harness
choice and Send, reply, expanded tool activity, patch, named sharing and a
teammate continuation. The same reply/patch crosses scene boundaries. White,
dark product and sage team acts replace repeated headline-on-dark slides.
Whole-workspace views establish context; critical text gets its own close-up.
The light end card uses an original-logo colour variant for contrast.

## Checks and corrections

- Latest full check `3e4f1001-ebed-49db-83d3-ed3cc32d941c` passed with zero
  lint, runtime, layout, contrast or craft warnings/errors, at 25 layout samples.
- Inspected the beat/phone sheets and all five seam strips; eight-frame
  sampling also examined the whole-app to reply bridge. Targeted cursor
  frames checked harness, Send, tool and sharing actions.
- Fresh read-only review found no P0 but found an empty app before the 8 s
  cut. Fixed: the same captured message and patch remain in their assembled
  conversation surface on both sides of 8 s. Re-snapshotted 7.9 and 8.1 s.
- Corrected a prematurely visible incoming panel, overlapping opening
  fragments and cursor positions. The named-share button now receives the
  cursor rather than a point outside its hit area.
- Strict render initially flagged missing stable shot IDs. Added explicit
  IDs; the subsequent `--strict-all` render passed.
- Narrow audit exceptions cover decorative line numbers and repeated
  carry-across text/viewport content already read in the preceding shot.
  They do not exempt the new hero reply, tool output, patch or search decision.
  End claim/CTA holds are deliberate. No unresolved P0 is known.

## Render

`renders/nexus-product-led-v2-preview.mp4`: 1920×1080, 30 fps, 60 seconds,
4,848,912 bytes. Rendered with two workers using screenshot capture/hardware
GPU in 1m45s. This is a motion preview, not the final music master.
ffprobe confirmed 1,800 H.264 frames and 60.000 seconds, with stereo AAC.
Inspected eight decoded MP4 frames spanning harness choice, tools, diff,
named sharing, teammate continuation, retrieval and the end card; see
`renders/review-v2-decoded.jpg`.

Bundled click, typing and transition effects are placed on visible causes.
No music bed or voiceover is present. Audio is checked by measurement, not
listened to; do not describe it as auditioned or final-mixed. Final soundtrack
selection, beat alignment and full mix QA remain open before landing-page use.
Measured true peak −18.8 dBTP, no clipped samples, integrated −39.3 LUFS
because the preview contains isolated effects and long gaps, not a music bed.
Do not normalize those isolated clicks to a final music-mix loudness target.

Weakest shot: the compact whole-workspace views contain secondary labels that
are not phone-sized reading copy. More production time should go into a
tighter harness/control macro, not more slogans or decorative effects. The
opening patch establishes its identity; the later patch close-up carries the
readable proof. The product fixtures remain explicitly illustrative.
