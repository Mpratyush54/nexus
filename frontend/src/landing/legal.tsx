import { ArrowLeft, Download, Moon, Sun } from 'lucide-react'
import type { ReactNode } from 'react'
import {
  CONTACT_EMAIL,
  DOWNLOAD_URL,
  GITHUB_URL,
  ISSUES_URL,
  LEGAL_UPDATED,
  PRIVACY_PATH,
  SECURITY_URL,
  SIGN_IN_PATH,
  TERMS_PATH,
} from './content'
import { SiteFooter } from './SiteFooter'

/*
 * /privacy and /terms. Rendered to static HTML by scripts/prerender.tsx, in
 * the landing page's style, with no React in the browser.
 *
 * Every claim in the privacy text should match what the code does. Where it
 * comes from:
 * - capture: internal/daemon/harvester.go (one harvester per workspace folder
 *   the user adds; SetPaused), the desktop setup wizard's Privacy step
 * - redaction before upload: internal/daemon/interceptor.go (RedactSecrets),
 *   internal/secrets/redact_text.go
 * - error reports: src/lib/diagnostics.ts (web) and the server's
 *   internal/diagnostics + migration 052 (30-day retention, no IP stored,
 *   user_id cleared when the account is deleted)
 * - embeddings: internal/context/embed_provider.go (configurable provider)
 * - email: internal/mail (Resend or SMTP, sign-in codes only)
 * The owner must review this text before launch.
 */

export type LegalKind = 'privacy' | 'terms'

export const LEGAL_META: Record<LegalKind, { path: string; title: string; heading: string; description: string }> = {
  privacy: {
    path: PRIVACY_PATH,
    title: 'Privacy Policy · Nexus',
    heading: 'Privacy Policy',
    description:
      'What Nexus captures, where it goes, who can see it, how long it is kept, and how to have it deleted. Written in plain language.',
  },
  terms: {
    path: TERMS_PATH,
    title: 'Terms of Use · Nexus',
    heading: 'Terms of Use',
    description: 'The terms for using the Nexus desktop app, web app and cloud service during the beta.',
  },
}

function Contact() {
  return (
    <ul>
      <li>
        Questions, requests and deletion: open an issue at <a href={ISSUES_URL}>github.com/Mpratyush54/nexus/issues</a>. Please don’t post
        personal details there; say what you need and we will follow up privately to confirm it is your account.
      </li>
      <li>
        Security problems: report them privately through a <a href={SECURITY_URL}>GitHub security advisory</a>, not a public issue.
      </li>
      {CONTACT_EMAIL && (
        <li>
          Email: <a href={`mailto:${CONTACT_EMAIL}`}>{CONTACT_EMAIL}</a>
        </li>
      )}
    </ul>
  )
}

function Sec({ id, title, children }: { id: string; title: string; children: ReactNode }) {
  return (
    <section id={id} aria-labelledby={`${id}-h`}>
      <h2 id={`${id}-h`}>{title}</h2>
      {children}
    </section>
  )
}

function Privacy() {
  return (
    <>
      <div className="nx-legal-summary">
        <h2>The short version</h2>
        <ul>
          <li>Nexus captures AI agent chats only from the folders you add, and you can pause it at any time.</li>
          <li>What it captures syncs to your own account. Nobody else sees it unless you share it.</li>
          <li>Common secrets are removed on your computer before anything is uploaded.</li>
          <li>Automatic error reports are anonymous and never contain your chats, memories, files or tokens. They are deleted after 30 days.</li>
          <li>We don’t sell your data, show ads or use tracking scripts.</li>
          <li>Ask and we will delete your account and everything in it.</li>
        </ul>
      </div>

      <Sec id="what" title="What Nexus captures">
        <p>
          The Nexus desktop app reads the chat history that coding agents such as Claude Code, Codex, Cursor, OpenCode, Gemini and Kimi
          already save on your computer. It only looks at chats that belong to the folders (workspaces) you add in the app. You choose the
          folders during setup and can add or remove them in Settings. You can pause capture from the tray icon at any time.
        </p>
        <p>From those chats Nexus keeps:</p>
        <ul>
          <li>the messages between you and the agent;</li>
          <li>the agent’s work: files it read, edits and their diffs, commands it ran and their results;</li>
          <li>token counts and estimated cost per chat, agent and model;</li>
          <li>basic project details, such as the folder name and the Git remote, so chats from the same project are grouped together.</li>
        </ul>
        <p>
          Nexus also keeps the memories it learns from those chats, the notes you write yourself, and the project brief. Chats you start in the
          Nexus app itself are stored the same way.
        </p>
      </Sec>

      <Sec id="secrets" title="Secrets and sensitive text">
        <p>
          Before a chat leaves your computer, Nexus replaces things that look like secrets, such as API keys, access tokens, private keys and
          password assignments, with <code>[REDACTED]</code>. This works by recognising common patterns, so it can miss a secret in an unusual
          format. Please don’t paste secrets into agent chats you capture.
        </p>
      </Sec>

      <Sec id="where" title="Where your data goes">
        <ul>
          <li>
            <strong>Your account.</strong> Captured chats and memories upload to your account on the Nexus server. If you are offline they wait on
            your computer and upload later. The hosted service runs on Amazon Web Services.
          </li>
          <li>
            <strong>Search.</strong> To let you and your agents search memories by meaning, memory text can be sent to an embedding service that
            turns it into numbers used for search. It is sent for that purpose only.
          </li>
          <li>
            <strong>Email.</strong> We use your email address to send sign-in and verification codes, through an email delivery provider. We
            don’t send marketing email.
          </li>
          <li>
            <strong>Notifications.</strong> If you turn on browser notifications, your browser’s push service delivers them.
          </li>
        </ul>
        <p>We don’t sell your data, share it with advertisers, or put analytics or tracking scripts on the website or in the web app.</p>
      </Sec>

      <Sec id="who" title="Who can see it">
        <ul>
          <li>Your chats and personal memories are private to you.</li>
          <li>
            Sharing a chat with a person, a team or an organisation is a separate action each time. Joining an organisation doesn’t reveal your
            private chats. You can remove anyone’s access later, and it ends straight away.
          </li>
          <li>
            The people who run Nexus can access stored data only when needed to keep the service running, fix a problem you reported, or meet a
            legal obligation.
          </li>
        </ul>
      </Sec>

      <Sec id="reports" title="Error reports">
        <p>When something breaks, Nexus can send a short error report so it can be fixed. There are two kinds.</p>
        <ul>
          <li>
            <strong>Automatic error reports</strong> are sent when a screen crashes or an unexpected error happens. They are sent without your
            account, so they are anonymous. They are on by default and you can turn them off in the web app under Settings, Privacy. The desktop
            app asks during setup, and when they are off it asks each time and shows you exactly what would be sent.
          </li>
          <li>
            <strong>Report a problem</strong> is something you send yourself. It includes the note you write and is linked to your account so we
            can reply. You see everything in it before you send it.
          </li>
        </ul>
        <p>An error report contains:</p>
        <ul>
          <li>the error message and where in Nexus’s code it happened;</li>
          <li>the app version, and your operating system and browser;</li>
          <li>
            the last few lines of Nexus’s own activity log, such as which page was open or which request failed, without their contents.
          </li>
        </ul>
        <p>
          It never contains your chats, memories, files, page text, request contents, passwords or tokens. Reports are cleaned on your device
          and again on the server to remove tokens, email addresses and the user name in file paths. The server doesn’t store your IP address
          with a report. Reports are deleted automatically after 30 days.
        </p>
      </Sec>

      <Sec id="keep" title="How long we keep data">
        <ul>
          <li>Your account, chats and memories are kept until you delete them or ask us to delete your account.</li>
          <li>Error reports are kept for 30 days.</li>
          <li>
            If your account is deleted, any error report you sent is no longer linked to you. Deleted data can stay in backups for a short time
            before those are replaced.
          </li>
        </ul>
      </Sec>

      <Sec id="choices" title="Your choices">
        <ul>
          <li>Choose which folders Nexus captures, remove a folder, or pause capture at any time.</li>
          <li>Turn automatic error reports off.</li>
          <li>Remove access you have given to anyone.</li>
          <li>Edit or delete memories in the app.</li>
          <li>
            Ask for a copy of your data, or for your account and everything in it to be deleted. When you uninstall the desktop app it asks
            whether to delete Nexus’s data from that computer too.
          </li>
        </ul>
      </Sec>

      <Sec id="children" title="Children">
        <p>Nexus is a tool for software developers and is not meant for children under 16.</p>
      </Sec>

      <Sec id="changes" title="Changes to this policy">
        <p>
          If we change what Nexus collects or how it is used, we will update this page and its date. For important changes we will also point
          them out in the app.
        </p>
      </Sec>

      <Sec id="contact" title="Contact">
        <Contact />
      </Sec>
    </>
  )
}

function Terms() {
  return (
    <>
      <div className="nx-legal-summary">
        <h2>The short version</h2>
        <ul>
          <li>Nexus is in beta. It is free to use, and it may change or break.</li>
          <li>Your chats, code and memories stay yours.</li>
          <li>Use Nexus lawfully, and only capture chats you are allowed to store.</li>
          <li>
            How we handle your data is in the <a href={PRIVACY_PATH}>Privacy Policy</a>.
          </li>
        </ul>
      </div>

      <Sec id="agree" title="Using Nexus">
        <p>
          These terms cover the Nexus desktop app, the web app and the Nexus cloud service. By creating an account or using Nexus you agree to
          them. If you use Nexus for an organisation, you confirm you are allowed to accept these terms for it.
        </p>
      </Sec>

      <Sec id="account" title="Your account">
        <ul>
          <li>Give a real email address and keep your sign-in details to yourself.</li>
          <li>You are responsible for what happens under your account and on the devices you link to it.</li>
          <li>Tell us straight away if you think someone else has access to your account.</li>
        </ul>
      </Sec>

      <Sec id="content" title="Your content">
        <p>
          Your chats, code, memories and notes belong to you. You let us store, process and display them only as needed to run Nexus for you
          and for the people you share them with. That permission ends when you delete the content or your account.
        </p>
        <p>
          Only capture chats and code you have the right to store. If a project belongs to your employer or a client, make sure their rules
          allow it.
        </p>
      </Sec>

      <Sec id="rules" title="Fair use">
        <p>Please don’t:</p>
        <ul>
          <li>break the law, or use Nexus to store or share content you have no right to;</li>
          <li>try to get into accounts or data that aren’t yours, or get around limits or security;</li>
          <li>disrupt the service, overload it, or probe it for weaknesses outside a responsible security report.</li>
        </ul>
        <p>We may suspend accounts that put the service or other people at risk.</p>
      </Sec>

      <Sec id="agents" title="AI agents and other services">
        <p>
          Nexus works with coding agents made by other companies, such as Claude Code, Codex, Cursor, OpenCode, Gemini and Kimi. Their own terms
          and privacy policies apply to how you use them. Nexus doesn’t control what an agent does, and you remain responsible for reviewing an
          agent’s changes before you rely on them.
        </p>
      </Sec>

      <Sec id="beta" title="Beta, plans and changes">
        <ul>
          <li>Nexus is a beta. Features can change, and some may be removed.</li>
          <li>The free plan has limits, shown in the web app. Paid plans are not open yet; if they open, prices will be shown before you pay.</li>
          <li>We may update these terms. We will change the date above and tell you in the app about important changes.</li>
        </ul>
      </Sec>

      <Sec id="ending" title="Ending your use">
        <p>
          You can stop using Nexus at any time and ask us to delete your account. We may end or suspend access if these terms are broken, or if
          we shut the service down, in which case we will give reasonable notice so you can export your data where possible.
        </p>
      </Sec>

      <Sec id="warranty" title="No warranty, limited liability">
        <p>
          Nexus is provided “as is” and “as available”, without warranties of any kind, to the extent the law allows. Keep your own backups of
          anything important. To the extent the law allows, we are not liable for indirect or consequential losses, or for lost data, profits
          or work, arising from your use of Nexus. Nothing in these terms limits rights you have under laws that can’t be excluded.
        </p>
      </Sec>

      <Sec id="contact" title="Contact">
        <Contact />
      </Sec>
    </>
  )
}

export function LegalPage({ kind }: { kind: LegalKind }) {
  const meta = LEGAL_META[kind]
  return (
    <div className="nx">
      <a className="nx-skip" href="#main">Skip to content</a>
      <header className="nx-nav">
        <div className="nx-wrap nx-nav-in">
          <a className="nx-brand" href="/" aria-label="Nexus home">
            <img src="/brand/nexus-app.svg" width="30" height="30" alt="" />
            <span>Nexus</span>
          </a>
          <div className="nx-nav-end">
            <button type="button" className="nx-theme" data-theme-toggle aria-label="Switch colour theme">
              <Sun className="i-sun" size={17} aria-hidden="true" />
              <Moon className="i-moon" size={17} aria-hidden="true" />
            </button>
            <a className="nx-legal-signin" href={SIGN_IN_PATH}>Sign in</a>
            <a className="nx-btn nx-btn-primary nx-btn-sm" href={DOWNLOAD_URL} aria-label="Download Nexus for Windows">
              <Download size={15} aria-hidden="true" /> Download
            </a>
          </div>
        </div>
      </header>
      <main id="main" className="nx-legal">
        <div className="nx-wrap nx-legal-in">
          <a className="nx-legal-back" href="/">
            <ArrowLeft size={15} aria-hidden="true" /> Back to Nexus
          </a>
          <header className="nx-legal-head">
            <p className="nx-label">{kind === 'privacy' ? 'privacy' : 'terms'}</p>
            <h1>{meta.heading}</h1>
            <p className="nx-legal-date">Last updated: {LEGAL_UPDATED}</p>
            <p className="nx-legal-other">
              {kind === 'privacy' ? (
                <>
                  See also the <a href={TERMS_PATH}>Terms of Use</a>.
                </>
              ) : (
                <>
                  See also the <a href={PRIVACY_PATH}>Privacy Policy</a>.
                </>
              )}{' '}
              Nexus is open source: you can read the code behind every statement here on <a href={GITHUB_URL}>GitHub</a>.
            </p>
          </header>
          <article className="nx-legal-body">{kind === 'privacy' ? <Privacy /> : <Terms />}</article>
        </div>
      </main>
      <SiteFooter />
    </div>
  )
}
