/**
 * How people get Nexus Desktop. The main action is the same download the
 * landing page offers; the PowerShell one-liner is a fallback for people who
 * prefer a terminal.
 *
 * nexus.pratyushes.dev/install.ps1 redirects to install-windows.ps1 on the
 * latest GitHub release. The script downloads and verifies Nexus-Setup.exe,
 * runs it, and puts the `nexus` command on PATH.
 */
export { DOWNLOAD_URL as DESKTOP_DOWNLOAD_URL } from '@/landing/content'

export const DESKTOP_INSTALL_PS1 = 'irm https://nexus.pratyushes.dev/install.ps1 | iex'

/** Platforms shown next to Windows so Mac and Linux users know where they stand. */
export const DESKTOP_COMING_SOON = ['macOS', 'Linux'] as const
