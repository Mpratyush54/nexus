# Expanded film evidence and exclusions

- Native Memory recording: Nexus-2026-10-25-00-29-03.mkv. Synthetic cloud project only. Search re-ranked the checkout decision; Architecture narrowed it to one result.
- Team/MCP recording: Nexus-2026-10-25-00-39-35.mkv. Isolated real frontend/API, Avery and Maya only. Public assets exclude browser navigation/profile UI.
- Recipient/Settings recording: Nexus-2026-10-25-00-45-30.mkv. Acceptance visibly changed sent→accepted; Maya opened the original conversation and diff. Settings Export JSON returned an actual success toast.
- JSON-RPC proof: go run ./videos/nexus-demo --state-dir videos/nexus-demo/runtime/v7/state --action memory, using the generated synthetic state, not real account tokens. Real memory_search returned one cart decision; project_knowledge returned project and team rules and excluded the personal row. Regression tests passed.
- Known failure: the shared conversation's Fork button returned 'only the session owner can modify it'. No fork success is included in the film. This needs a product regression fix separately.
- Local demo limitations: account profile service unavailable in the in-memory fixture; no external harness execution channel, push keys or paid checkout. Those surfaces are not presented as completed workflows.
- OBS was verified stopped before editing. Native Nexus remains running with normal login configuration. No normal account credentials, membership, role, billing or capture-policy settings were edited.
- HyperFrames pin updated 0.8.192→0.8.193. The unchanged V6 source passed the newer check before expanded edits (0 errors, 14 known lint warnings).
