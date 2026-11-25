/**
 * Tab titles for app routes. index.html carries the landing page's title, so
 * without this every sign-in screen, 404 and app page read "Nexus: shared
 * memory for every AI coding agent".
 */
const TITLES: [RegExp, string][] = [
  [/^\/login\/?$/, 'Sign in'],
  [/^\/signup\/?$/, 'Create your account'],
  [/^\/forgot-password\/?$/, 'Reset your password'],
  [/^\/cli\/auth\/?$/, 'Approve the desktop app'],
  [/^\/app\/?(dashboard\/?)?$/, 'Home'],
  [/^\/app\/(chats|timeline)\/?$/, 'Chats'],
  [/^\/app\/memory\/?$/, 'Memory'],
  [/^\/app\/activity\/?$/, 'Activity'],
  [/^\/app\/devices\/?$/, 'Phone & devices'],
  [/^\/app\/team\/?$/, 'Team'],
  [/^\/app\/org(\/[^/]+)?\/?$/, 'Organization'],
  [/^\/app\/connect\/?$/, 'Desktop app'],
  [/^\/app\/onboarding\/?$/, 'Set up Nexus'],
  [/^\/app\/notifications\/?$/, 'Notifications'],
  [/^\/app\/settings\/?$/, 'Settings'],
  [/^\/app\/admin\/?$/, 'Platform'],
]

/** "Chats · Nexus", "Page not found · Nexus"; the demo says so too. */
export function routeTitle(pathname: string, demo = false): string {
  const hit = TITLES.find(([re]) => re.test(pathname))
  const page = hit ? hit[1] : pathname.startsWith('/app/') ? 'Nexus' : 'Page not found'
  const site = demo ? 'Nexus demo' : 'Nexus'
  return page === 'Nexus' ? site : `${page} · ${site}`
}
