; Nexus Windows installer (Inno Setup 6+)
; Build: ISCC scripts\nexus.iss  (after scripts\build-windows-release.ps1)

#define MyAppName "Nexus"
#define MyAppVersion "0.1.0"
#define MyAppPublisher "Nexus"
#define MyAppURL "https://nexus.pratyushes.dev"
#define MyAppExeName "nexus-desktop.exe"

[Setup]
AppId={{A7C3E9F1-2B4D-4E6A-9C1F-8D0E5B3A2F71}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
AppPublisherURL={#MyAppURL}
DefaultDirName={autopf}\Nexus
DefaultGroupName=Nexus
DisableProgramGroupPage=yes
OutputDir=..\dist
OutputBaseFilename=NexusSetup-{#MyAppVersion}
Compression=lzma
SolidCompression=yes
WizardStyle=modern
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
UninstallDisplayIcon={app}\nexus-desktop.exe

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "Create a desktop icon"; GroupDescription: "Additional icons:"
Name: "autostart"; Description: "Start Nexus Desktop when I sign in to Windows"; GroupDescription: "Startup:"

[Files]
Source: "..\dist\nexus-windows-amd64.exe"; DestDir: "{app}"; DestName: "nexus.exe"; Flags: ignoreversion
Source: "..\dist\nexus-daemon-windows-amd64.exe"; DestDir: "{app}"; DestName: "nexus-daemon.exe"; Flags: ignoreversion
Source: "..\dist\nexus-desktop-windows-amd64.exe"; DestDir: "{app}"; DestName: "nexus-desktop.exe"; Flags: ignoreversion

[Icons]
Name: "{group}\Nexus Desktop"; Filename: "{app}\{#MyAppExeName}"
Name: "{group}\Uninstall Nexus"; Filename: "{uninstallexe}"
Name: "{autodesktop}\Nexus Desktop"; Filename: "{app}\{#MyAppExeName}"; Tasks: desktopicon
Name: "{userstartup}\Nexus Desktop"; Filename: "{app}\{#MyAppExeName}"; Tasks: autostart

[Run]
Filename: "{app}\{#MyAppExeName}"; Description: "Launch Nexus Desktop"; Flags: nowait postinstall skipifsilent

[Code]
procedure CurStepChanged(CurStep: TSetupStep);
begin
  if CurStep = ssPostInstall then
  begin
    // Ensure user-local bin shim path for CLI
  end;
end;
