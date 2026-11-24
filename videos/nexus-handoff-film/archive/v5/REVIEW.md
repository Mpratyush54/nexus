# V5 review — connected product camera

The user rejected V4 because static crops and hard cuts did not deliver the requested motion. V5 replaces that edit with continuous spatial camera choreography over the actual Nexus recording. V4 and its review are preserved in archive/v4 and renders/nexus-one-decision-v4.mp4. Nothing has been published or swapped into the landing page.

## Creative decisions

- One native app world per act; nineteen eased camera moves. The opening travels from the full window into the prompt, pans down to the decision and code, pulls back, follows the Cursor→Codex selector and composer, then moves into the actual reply. The team act follows Share, the dialog, Maya, Send and the native three-file confirmation. The whole app pulls out and collapses toward the original Nexus mark.
- The app occupies the upper 900px. A restrained 180px caption band supplies a readable narrative, with short per-word clip reveals. No duplicate vector “product UI,” artificial token animation, decorative camera drift, or fake cursor.
- Revisited official Apple MacBook Pro and iPhone Air product-film frame studies. The borrowed direction is product continuity, macro-to-whole movement and purposeful reading holds—not Apple assets, nor a claim of equivalent Apple production quality. Source pages: https://www.apple.com/macbook-pro/ and https://www.apple.com/iphone-air/ .
- Used the HyperFrames ui-focus-zoom camera-wrapper pattern and caption-clip-wipe component pattern. Project tooling upgraded 0.8.191→0.8.192; upgrade verification passed.
- User-selected energetic electronic track remains EDM Detection Mode by Kevin MacLeod, 128BPM, source120s, with two subdued UI clicks aligned to the recorded Share and Send actions. No voiceover. CC BY4.0 credit retained in film and asset manifest. Sound is measured, not auditioned.

## Evidence and limits

The isolated checkout demo uses seeded synthetic Cursor turns followed by a genuine in-app Codex request/reply. Actual Share chat+code, Maya(Demo) and Send were recorded; native confirmation and a read-only inbox API check confirmed a pending transfer with three source files. User approved ending at sending; recipient acceptance/restore are not shown or implied. No private project conversations or credentials were added.

The source is 1920×1020 native raster, recorded at 30fps. Close-ups enlarge existing pixels up to 2×; a camera move cannot create missing source detail. These native small-text shots remain the weakest point, especially at phone width. Higher-density original capture would improve them. The captions carry the mobile story, not every native label. Native dropdown popup windows were absent from window-only capture, so their waiting gaps were cut out; no menu was reconstructed.

## Review performed

- Final check 66065108-af9c-46c4-b82b-62b169a8118a, revision 8c99fc4c-552f-4ab5-98f5-ec3998e4b168: passed, with zero lint/runtime/layout/contrast errors and zero craft warnings.
- Fourteen intentionally retained lint warnings: thirteen nonzero nested media/image starts are composition-local (not legacy global timestamps); the caption track contains sequential short clips. Source GOP interval 8.33s can slow preview seeking but does not affect encoded render timing.
- Inspected both beat sheets, both seam sheets and both 360px phone sheets, plus eight consecutive frames of the opening move and eight of the Share-to-dialog move. Seam into the brand stays on the same light ground; no black interstitial.
- Independent fresh-eyes review found no actionable P0/P1. It confirmed continuous opening/Share motion; clipped transitional frames resolve into their intended framing.
- Rendered twelve-second proof: 1920×1080, 30fps, H.264/AAC48kHz stereo, 12.000s, 3,150,793bytes. Decoded consecutive frames from the actual MP4 confirm the camera transform survived rendering.

## Verified full-film encode

- Output: renders/nexus-one-decision-v5.mp4. H.264,1920×1080,30fps,60.000s; AAC48kHz stereo;16,127,161bytes. Rendered with hardware-GPU screenshot capture in2m42.7s.
- Decoded the encoded beat frames at0,1.5,9,13,22,35,43.2,47,54,56.2,58 and59.93s. The native code, genuine reply, selected recipient, sent confirmation and final platform copy appear. The near-white final pre-brand frame retains its narrative caption while the app fades; the mark appears immediately at56.25s.
- Encoded audio measured−19.2 integrated LUFS,−1.7dB true peak,zero clipped samples. Musical fit has not been auditioned.
- SHA256:3B74D78277C1C1FEBAF4182E3F43CF09A75DEC3172F0C7F9B89ECC589205296F.

No public deployment, push or release is part of this revision. Normal Nexus was not restarted or reconfigured during this camera-only revision.

Editable preview: http://127.0.0.1:4570/#project/nexus-handoff-film
