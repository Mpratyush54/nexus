# Nexus Desktop 0.4 beta

Windows x64 prerelease. This is not a stable release and is not code-signed.
Windows SmartScreen may warn; verify the SHA-256 against the release assets.

Extract the ZIP into a new, versioned folder, then run `NexusBeta/Nexus.exe`.
Do not put the CLI over the GUI: Windows treats `Nexus.exe` and `nexus.exe` as
the same filename. The CLI is provided separately under `NexusBeta/cli/`.
Do not overwrite a running installation. Quit Nexus first.

The package includes .NET and Windows App SDK runtime files. Windows 10 version
1809 or newer (x64) is required. An installed, signed-in harness CLI is needed
to continue chats; Nexus does not supply a model subscription or bypass quota.

## In this beta

- New shared Nexus branding for window, executable and tray.
- Explicit tray Quit uses bounded shutdown; closing the window can hide it.
- Readable, selectable chat, tool details and saved file patches.
- Model discovery and streaming through the supported native harnesses.
- Optional authenticated Nexus Web execution: off by default in Settings.
  Every remote message requires local approval for a mapped workspace.
  Turning it off stops remote work. No localhost execution port is opened.
- Auto-start remains an explicit Settings choice.

## Limits and safety

- Browser execution also requires the matching server deployment and migration
  039. A desktop-only update cannot enable the cloud channel by itself.
- Browser requests currently seed the selected harness with the recent
  authorized conversation context; they do not resume an arbitrary remote
  machine's native session ID. One remote request per device at a time.
- Historical filename-only captures cannot reconstruct patches never saved.
- A cancelled request does not undo changes already made by the agent.
- Approving a remote turn authorizes that harness to read/modify the workspace
  with its installed permissions; review the requested message before allowing.
- The beta and stable update feeds must remain separate. Install a newer beta
  explicitly; do not promote this package to the stable `latest.json`.

Keep a backup of important work and report reproducible issues with the app
version, harness and error text. Do not post credentials or private transcripts.
