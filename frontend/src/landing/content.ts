export const SITE_URL = 'https://nexus.pratyushes.dev'
// Redirects (Cloudflare rule) to releases/latest/download/Nexus-Setup.exe.
export const DOWNLOAD_URL = 'https://nexus.pratyushes.dev/download'
// macOS beta (Apple silicon): the fixed-name DMG on the newest release
// (release-desktop.yml). Unsigned until Developer ID signing is set up.
export const MAC_DOWNLOAD_URL = 'https://github.com/Mpratyush54/nexus/releases/latest/download/Nexus.dmg'
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
  'Nexus is a native desktop app and cloud server that gives Claude Code, Codex, Cursor, OpenCode, Gemini and Kimi one shared memory and one chat. Agents keep working in the background, ask before they act, and every change can be reviewed or undone.'

// The newest desktop release the page describes (the "New in" note and the
// structured data). Bump it with each release that changes what the page shows.
export const LATEST_VERSION = '0.4.5'

// What the app can do, in plain words. Feeds the SoftwareApplication
// featureList in the prerendered JSON-LD, so keep it to shipped features.
export const FEATURES = [
  'Chat with Claude Code, Codex, Cursor, OpenCode, Gemini and Kimi in one app',
  'Agents keep working when you switch chats, with several chats running at once',
  'Status dots for working, waiting and new replies, and a notification when an agent is done',
  'Permission modes, approvals inside the chat and "always allow" rules',
  'Review changes: keep or undo each edit an agent made',
  'Checkpoints: restore to before any message, or type /rewind',
  '@ mentions for files, chats and memories, / commands and ↑ to recall a message',
  'A model picker with a model per chat',
  'Voice input on Windows, macOS and the web',
  'Quick ask from anywhere with Alt+Space (Option+Space on a Mac)',
  'Live thinking and streaming replies on the desktop and on the web',
  'Edit CLAUDE.md and AGENTS.md rules, and add, test and switch MCP tool servers',
  'One shared, searchable project memory every agent can use',
  'A per-line history of which agent changed a line, and why',
]

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
    a: 'Nexus is a desktop app for Windows (with a macOS beta) plus a cloud server. You chat with all your AI coding agents in one place, and what they learn becomes one shared project memory that every agent can look things up in. There is a free plan; paid plans are not open yet.',
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
    q: 'Can I undo what an agent changed?',
    a: 'Yes. After a reply, Review changes shows every edit the agent made, and you keep or undo each file or each change inside it. You can also restore a chat to before any message, files included, or type /rewind.',
  },
  {
    q: 'What happens to secrets and private data?',
    a: 'Secrets are redacted and access tokens are masked before anything is stored. You choose which workspaces Nexus captures and can pause capture at any time. Personal memory stays private to you; only what you share with a project or team is visible to others, and access can be revoked. Teams can share chats, create organisations and hand a chat to a teammate.',
  },
  {
    q: 'Is Nexus available for macOS or Linux?',
    a: 'The desktop app is a beta for Windows and for Macs with Apple silicon. Linux has no desktop app yet. The Nexus web app works in any modern browser, phones included, for chats, memory, sessions and team settings.',
  },
]
