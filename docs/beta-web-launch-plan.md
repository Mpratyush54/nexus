# Nexus beta, web parity and launch film

User scope, 23 October 2026:

1. Design a new Nexus vector mark and use one brand across desktop, tray, web, landing page and video.
2. Fix explicit tray Quit: stop capture/continuations, remove tray/IPC resources, and guarantee bounded process termination. Closing the window may still hide to tray.
3. Ship the latest native Windows app as an explicitly labeled prerelease. Keep stable downloads and update pointers unchanged. Publish checksums, prerequisites and beta limitations.
4. Bring the desktop conversation structure to the web: project-scoped searchable/paginated chats, readable messages and tool details, file diffs, search within a conversation, prompt navigation and visible lineage. User explicitly approved the authenticated browser-to-desktop execution channel. Use outbound HTTPS, a separate device credential, opt-in Settings and per-turn local approval, not the retired localhost bridge.
5. Redesign the landing page around product clarity, real UI, a clear beta download path, privacy/consent and team collaboration.
6. Create a new 60-second HyperFrames product film: both multi-agent work and shared team context; fast, restrained Apple-inspired SaaS motion; music and subtle UI sounds; no voiceover. Review a short storyboard before animation production.

Verification: Go race tests and native shutdown/parser unit tests; Windows build with the newly built native core; actual process-exit smoke check without GUI automation; web typecheck/build/lint and focused behavior tests; video composition checks and render-frame review.

Publication: GitHub authentication is available. AWS sign-in was refreshed and verified on 24 October; this does not establish deployment. Do not claim S3 publication or change stable release feeds without a verified upload. Do not publish local caches, credentials, old binaries or unrelated build outputs.

Progress

- Prior desktop tool streaming and file-diff builds are working locally.
- Native checks pass. On 24 October beta.3 exited with code 0 in 5.50 seconds after IPC invoked the same Quit path as the tray. No force-kill was used for that test. Beta.3 was relaunched and is responding. No visual UI automation was used.
- Browser execution protocol, durable Postgres migration, native worker/approval UI and web chat controls are implemented locally. Security/race tests and a disposable PostgreSQL concurrency/durability test passed. Signed-in end-to-end deployment acceptance remains required.
- Conversation reads now support latest-first page loading and lossless string keys for historical turn indexes larger than JavaScript's safe-integer range.
- New landing page and interactive real-component demonstration are implemented locally; no new film URL is exposed before a verified render.
- Video storyboard approved: scattered to shared. Music provider is signed out; user asked what the service is and was told HeyGen. Soundtrack source remains undecided.
- A self-contained Windows beta ZIP has been built locally. It is not yet published, and later source edits require a final rebuild before publication.
- No new beta artifact or web deployment has been published yet.

Latest verification, 24 October:

- Self-contained Windows package: `dist/beta/0.4.0-beta.3/`; built with current
  native sources. Manifest explicitly records an uncommitted source tree;
  publication still requires a reproducible source snapshot. Quit process-exit
  smoke test has now passed and the beta was relaunched.
- 39 native unit tests, targeted Go race suites, tagged native-core tests and
  disposable Postgres queue concurrency/durability checks passed.
- HTTPS live-stream test delivered a partial response before completion and
  reconnected without executing again. Six frontend tests and production build
  passed; existing unrelated lint warnings and main-bundle size warning remain.
- Browser UI prevents Send while discovering models and explains why a shared
  conversation must be forked before continuing. Server owner checks remain
  authoritative. SSE parsing now handles CRLF split across network reads.
- Film visual check: zero lint/runtime/layout/contrast/craft warnings/errors.
  Preview HTTP 200: `http://127.0.0.1:4567/#project/nexus-launch`.
  `videos/nexus-launch/REVIEW.md` records remaining soundtrack/render gates.
- No browser-to-Desktop real-harness acceptance against the deployed service
  has occurred. Browser image-attachment transport is not yet implemented.

Video revision, 24 October: user rejected the initial creative treatment.
Official Apple film references were researched; all scenes were rebuilt around
macro/whole-product reframing and continuous conversation/patch handoffs.
`videos/nexus-launch/renders/nexus-product-led-v2-preview.mp4` is a verified
60 s 1080p/30 fps motion preview with UI effects, not a final music master.
No beta release, API deployment, web deployment or stable-feed update has
been performed. AWS credentials are available again; signed-in rollout checks
and final soundtrack selection remain open.
