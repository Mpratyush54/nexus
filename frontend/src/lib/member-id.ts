// Adding someone to a project or org takes their user ID: the server has no
// username or email lookup for non-admins. These helpers catch the common
// mix-ups and say what to do instead of letting the request fail.

const USER_ID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const EMAIL = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

export const MEMBER_ID_HELP = 'Ask your teammate to open Settings → Profile and copy their user ID.'

/** Trims the input and strips the "User ID:" label or quotes people paste along with it. */
export function normalizeMemberId(raw: string): string {
  return raw
    .trim()
    .replace(/^user\s*id\s*:?\s*/i, '')
    .replace(/^["'`]+|["'`]+$/g, '')
    .trim()
}

/** Null when `id` is a valid user ID, otherwise a short explanation for a toast. */
export function memberIdProblem(id: string, opts: { emailInviteHere?: boolean } = {}): { title: string; detail: string } | null {
  if (USER_ID.test(id)) return null
  if (EMAIL.test(id)) {
    return {
      title: 'That’s an email address',
      detail: opts.emailInviteHere
        ? 'Use “Invite by email” below to send an invite link, or paste their user ID here.'
        : `Nexus can’t look people up by email here. ${MEMBER_ID_HELP} To invite by email, use your organization page.`,
    }
  }
  if (/^[0-9a-f-]+$/i.test(id)) {
    return { title: 'That user ID looks incomplete', detail: `User IDs have 36 characters, like 1b2c3d4e-5f60-…. ${MEMBER_ID_HELP}` }
  }
  if (/^@?[a-z0-9][a-z0-9._-]{0,63}$/i.test(id)) {
    return {
      title: 'Usernames can’t be looked up yet',
      detail: `Add people by user ID. ${MEMBER_ID_HELP}`,
    }
  }
  return { title: 'Enter a user ID', detail: `User IDs look like 1b2c3d4e-5f60-…. ${MEMBER_ID_HELP}` }
}
