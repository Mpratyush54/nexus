# Feature evidence for the next content cut

Source inspection2026-10-24. This is not runtime certification or recorded footage.

## MCP

- frontend/src/lib/mcp-config.ts builds cloud HTTPS /v1/agent/mcp configuration and local mem.exe mcp stdio configuration. Both use authentication and may bind a project/agent.
- internal/mcp/tools.go exposes memory_search, memory_write, memory_reflect, project_knowledge, session_summary_get and session_fetch, among other tools. memory_search returns active project memories; project_knowledge bootstraps project/organization knowledge and explicitly omits personal/session rows.
- frontend/src/pages/AgentsPage.tsx exposes agent modes Full, Propose only, Read only and Blocked, per-tool controls, bound-token generation and MCP activity.
- Safe wording: connect Nexus to compatible MCP clients and retrieve authorized context. Do not imply bypassing scopes or universal testing of every AI agent.

## Organizations

- frontend/src/pages/OrgPage.tsx and frontend/src/api/orgs.ts expose organizations, named membership/role management, projects, invitations, capture controls, storage/activity, share revocation, audit and offboarding preview/transfer. Billing is role-gated.
- desktop/windows/Pages/SettingsPage.Organizations.cs exposes Organizations & members, create/join invitations, named members and OWNER/ADMIN/MEMBER-aware controls. Native copy distinguishes live team shares from version-limited private snapshots.
- Safe story: shared projects and knowledge with controlled access. Source inspection does not establish every control works in production; capture/test the exact demonstrated flows first.

## Existing footage

assets/workflow-recording.mp4 proves the native agent switch, genuine Codex reply and recorded Send-to-Maya flow. It contains no MCP setup/retrieval or organization-management sequence. Do not relabel the existing footage as those missing demonstrations.

videos/nexus-demo uses actual server handlers with an isolated in-memory store and synthetic Avery/Maya identities. It is a suitable capture starting point, not proof of production deployment or a pre-existing MCP retrieval recording.
