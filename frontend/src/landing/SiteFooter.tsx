import {
  CONTACT_EMAIL,
  DEMO_PATH,
  DOWNLOAD_URL,
  GITHUB_URL,
  ISSUES_URL,
  PRIVACY_PATH,
  SECURITY_URL,
  SIGN_IN_PATH,
  SIGN_UP_PATH,
  TERMS_PATH,
  WEB_APP_PATH,
} from './content'

/*
 * The public-site footer, shared by the landing page and the static legal
 * pages (scripts/prerender.tsx). Plain markup: nothing here needs a script.
 */

const COLUMNS: { title: string; links: { label: string; href: string }[] }[] = [
  {
    title: 'Product',
    links: [
      { label: 'Download for Windows', href: DOWNLOAD_URL },
      { label: 'Live demo', href: DEMO_PATH },
      { label: 'Web app', href: WEB_APP_PATH },
      { label: 'Questions', href: '/#faq' },
    ],
  },
  {
    title: 'Account',
    links: [
      { label: 'Sign in', href: SIGN_IN_PATH },
      { label: 'Create a free account', href: SIGN_UP_PATH },
    ],
  },
  {
    title: 'Legal and contact',
    links: [
      { label: 'Privacy Policy', href: PRIVACY_PATH },
      { label: 'Terms of Use', href: TERMS_PATH },
      { label: 'Contact and help', href: ISSUES_URL },
      { label: 'Report a security issue', href: SECURITY_URL },
      ...(CONTACT_EMAIL ? [{ label: CONTACT_EMAIL, href: `mailto:${CONTACT_EMAIL}` }] : []),
    ],
  },
]

export function SiteFooter() {
  return (
    <footer className="nx-foot">
      <div className="nx-wrap nx-foot-grid">
        <div className="nx-foot-about">
          <a className="nx-brand" href="/" aria-label="Nexus home">
            <img src="/brand/nexus-app.svg" width="26" height="26" alt="" />
            <span>Nexus</span>
          </a>
          <p>Every coding agent in one app, with one shared memory. For Windows, a macOS beta and the web, with a free plan.</p>
        </div>
        <nav className="nx-foot-cols" aria-label="Footer">
          {COLUMNS.map((c) => (
            <div key={c.title}>
              <p className="nx-foot-h">{c.title}</p>
              <ul>
                {c.links.map((l) => (
                  <li key={l.label}>
                    <a href={l.href}>{l.label}</a>
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </nav>
      </div>
      <div className="nx-wrap nx-foot-base">
        <span>© {new Date().getFullYear()} Nexus</span>
        <a href={GITHUB_URL}>Source on GitHub</a>
      </div>
    </footer>
  )
}
