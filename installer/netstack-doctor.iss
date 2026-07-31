; Inno Setup script for the NetStack Doctor Windows installer.
;
; Installs the standalone GUI app and creates a Start Menu entry for it. The
; app is a single self-contained .exe (the WebView2 runtime ships with
; Windows 10/11), so there is nothing else to lay down.
;
; Requires Inno Setup 6.3+ (for the x64compatible architecture identifiers).
;
; Build:
;   iscc /DAppVersion=1.2.1 installer\netstack-doctor.iss
; or use scripts\make-windows-installer.ps1, which builds the binary first.

#ifndef AppVersion
  #define AppVersion "1.2.1"
#endif

#define AppName      "NetStack Doctor"
#define AppExe       "NetStack Doctor.exe"
#define AppPublisher "Proclaim Advisors"

[Setup]
; Never change AppId — it is what lets a new build upgrade an existing install.
AppId={{8F3A6B2E-1C4D-4E7A-9B15-2D6F0A3C7E48}
AppName={#AppName}
AppVersion={#AppVersion}
AppVerName={#AppName} {#AppVersion}
AppPublisher={#AppPublisher}
VersionInfoVersion={#AppVersion}
DefaultDirName={autopf}\{#AppName}
DefaultGroupName={#AppName}
UninstallDisplayName={#AppName}
UninstallDisplayIcon={app}\{#AppExe}
OutputDir=..\dist
OutputBaseFilename=NetStack-Doctor-{#AppVersion}-windows-amd64-setup
SetupIconFile=..\assets\NetStackDoctor.ico
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
; The binary is amd64; x64compatible also allows installing on ARM64 Windows,
; which runs it under emulation.
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
; Per-machine by default, but a user without admin rights can still install
; into their own profile rather than being turned away.
PrivilegesRequired=admin
PrivilegesRequiredOverridesAllowed=dialog
; The Start Menu folder is created from DefaultGroupName; the page that lets
; the user rename it is just noise for a single-shortcut app.
DisableProgramGroupPage=yes

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: unchecked

[Files]
Source: "..\{#AppExe}"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\README.md"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
; Start Menu entry, plus its uninstall companion, under the "NetStack Doctor" group.
Name: "{group}\{#AppName}"; Filename: "{app}\{#AppExe}"; Comment: "Diagnose all seven OSI network layers"
Name: "{group}\Uninstall {#AppName}"; Filename: "{uninstallexe}"
Name: "{autodesktop}\{#AppName}"; Filename: "{app}\{#AppExe}"; Tasks: desktopicon

[Run]
Filename: "{app}\{#AppExe}"; Description: "{cm:LaunchProgram,{#StringChange(AppName, '&', '&&')}}"; Flags: nowait postinstall skipifsilent
