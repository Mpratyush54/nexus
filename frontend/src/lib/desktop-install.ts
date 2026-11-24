/**
 * How people get Nexus Desktop. The main action is the same download the
 * landing page offers; the PowerShell one-liner is a fallback for people who
 * prefer a terminal.
 *
 * The one-liner still points at the release bucket (no Nexus-domain mirror
 * exists yet), so it is never the headline action.
 */
export { DOWNLOAD_URL as DESKTOP_DOWNLOAD_URL } from '@/landing/content'

const BUCKET = 'https://central-memory-releases.s3.ap-south-1.amazonaws.com/desktop/latest'

export const DESKTOP_INSTALL_PS1 = `irm ${BUCKET}/install-windows.ps1 | iex`

/** Platforms shown next to Windows so Mac and Linux users know where they stand. */
export const DESKTOP_COMING_SOON = ['macOS', 'Linux'] as const
