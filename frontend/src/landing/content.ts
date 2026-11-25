export const SITE_URL = 'https://nexus.pratyushes.dev'
export const DOWNLOAD_URL = 'https://github.com/Mpratyush54/nexus/releases/latest'
export const GITHUB_URL = 'https://github.com/Mpratyush54/nexus'
export const WEB_APP_PATH = '/app'
export const SIGN_IN_PATH = '/login'
export const SIGN_UP_PATH = '/signup'
// The interactive demo: the real web app on sample data, no account needed.
export const DEMO_PATH = '/demo'
// Static legal pages, prerendered by scripts/prerender.tsx (no React at runtime).
export const PRIVACY_PATH = '/privacy'
export const TERMS_PATH = '/terms'
export const LEGAL_UPDATED = '25 November 2026'
// Public contact points. Both mailboxes forward (Cloudflare Email Routing) to
// the owner; security reports go to a private GitHub security advisory.
export const ISSUES_URL = 'https://github.com/Mpratyush54/nexus/issues'
export const SECURITY_URL = 'https://github.com/Mpratyush54/nexus/security/advisories/new'
export const CONTACT_EMAIL = 'support@pratyushes.dev'
// Privacy and data requests (access, export, deletion).
export const PRIVACY_EMAIL = 'privacy@pratyushes.dev'
// Full product film: 1 min 26 s, music only (captions describe the sound).
export const FILM_DURATION = '1:26'

export const TITLE = 'Nexus: shared memory for every AI coding agent'
export const DESCRIPTION =
  'Nexus is a native Windows app and cloud server that gives Claude Code, Codex, Cursor, OpenCode, Gemini and Kimi one shared memory, one chat, and a per-line history of why code changed.'

export const AGENTS = [
  { id: 'claude', name: 'Claude Code' },
  { id: 'codex', name: 'Codex' },
  { id: 'cursor', name: 'Cursor' },
  { id: 'opencode', name: 'OpenCode' },
  { id: 'gemini', name: 'Gemini' },
  { id: 'kimi', name: 'Kimi' },
] as const

export type AgentId = (typeof AGENTS)[number]['id']

export const FAQ: { q: string; a: string }[] = [
  {
    q: 'What is Nexus?',
    a: 'Nexus is a native Windows desktop app plus a cloud server. It lets you chat with your AI coding agents in one place, captures their sessions into a searchable timeline, and turns what they learn into a shared project memory that every agent can look things up in.',
  },
  {
    q: 'Which AI coding agents does Nexus work with?',
    a: 'Claude Code, Codex, Cursor, OpenCode, Gemini and Kimi. You can chat with each of them from the Nexus app, pick a model per agent, and Nexus captures sessions from all of them, including ones you run outside the app.',
  },
  {
    q: 'How do agents use the shared memory?',
    a: 'With one click, Nexus connects each agent to your shared memory (using MCP, the standard way agents get extra tools). Agents can then search memory, see a file’s history and ask why a line changed, and they read a short project brief first, so a new session starts with what earlier sessions decided instead of from zero.',
  },
  {
    q: 'What does "why" for a line of code mean?',
    a: 'Nexus records which agent session changed each line and the reasoning behind it. The history is tied to the line itself rather than its line number, so it follows the code when lines move. Ask the why tool, or open a file in Nexus, and you see the session and the decision behind the change.',
  },
  {
    q: 'What happens to secrets and private data?',
    a: 'Secrets are redacted and access tokens are masked before anything is stored. You choose which workspaces Nexus captures and can pause capture at any time. Personal memory stays private to you; only what you share with a project or team is visible to others, and access can be revoked.',
  },
  {
    q: 'Can I use Nexus with my team?',
    a: 'Yes. You can share sessions, create organisations, grant and revoke access, and teleport a session to a teammate or to another of your machines so the work continues with its full context.',
  },
  {
    q: 'Is Nexus available for macOS or Linux?',
    a: 'The desktop app is a Windows beta today, and macOS support is planned. The Nexus web app works in any modern browser for browsing memory, sessions and team settings.',
  },
  {
    q: 'How much does Nexus cost?',
    a: 'There is a free plan. Paid plans are not open yet; current plan limits are shown in the web app.',
  },
]
