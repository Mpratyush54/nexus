/** Official one-liners — install tray, daemon, CLI, and OS login/startup hooks. */

const BUCKET =
  'https://central-memory-releases.s3.ap-south-1.amazonaws.com/desktop/latest'

export const DESKTOP_INSTALL_PS1 =
  `irm ${BUCKET}/install-windows.ps1 | iex`

export const DESKTOP_INSTALL_MACOS =
  `curl -fsSL ${BUCKET}/install-macos.sh | bash`

export const DESKTOP_INSTALL_LINUX =
  `curl -fsSL ${BUCKET}/install-linux.sh | bash`

export type DesktopOS = 'windows' | 'macos' | 'linux'

export type DesktopInstallOption = {
  id: DesktopOS
  label: string
  hint: string
  command: string
  shell: string
}

export const DESKTOP_INSTALL_OPTIONS: DesktopInstallOption[] = [
  {
    id: 'windows',
    label: 'Windows',
    hint: 'PowerShell · Start Menu + Startup',
    command: DESKTOP_INSTALL_PS1,
    shell: 'PowerShell',
  },
  {
    id: 'macos',
    label: 'macOS',
    hint: 'Terminal · LaunchAgent at login',
    command: DESKTOP_INSTALL_MACOS,
    shell: 'Terminal',
  },
  {
    id: 'linux',
    label: 'Linux',
    hint: 'bash · .desktop + autostart',
    command: DESKTOP_INSTALL_LINUX,
    shell: 'bash',
  },
]

export function detectDesktopOS(): DesktopOS {
  if (typeof navigator === 'undefined') return 'windows'
  const ua = navigator.userAgent.toLowerCase()
  if (ua.includes('mac')) return 'macos'
  if (ua.includes('linux')) return 'linux'
  return 'windows'
}
