; OfficeChat Windows installer (NSIS). Built by build.sh:
;   makensis -DVERSION=x.y.z -DSRC=<dir with exes> -DICON=<ico> -DOUT=<setup.exe> installer.nsi
; Installs to Program Files, adds Start Menu + Desktop shortcuts, an entry in
; Settings > Apps (with uninstaller), and Windows Firewall rules, so the app
; works between Windows and Mac without any manual firewall steps.

Unicode true
!include "MUI2.nsh"
!include "x64.nsh"
!include "FileFunc.nsh"
!include "LogicLib.nsh"

!define APP "OfficeChat"
!define UNKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP}"

Name "${APP}"
OutFile "${OUT}"
InstallDir "$PROGRAMFILES64\${APP}"
InstallDirRegKey HKLM "Software\${APP}" "InstallDir"
RequestExecutionLevel admin
SetCompressor /SOLID lzma
BrandingText "${APP} ${VERSION}"
ManifestDPIAware true

VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "${APP}"
VIAddVersionKey "CompanyName" "${APP}"
VIAddVersionKey "FileDescription" "${APP} Setup"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" " "

!define MUI_ICON "${ICON}"
!define MUI_UNICON "${ICON}"
!define MUI_ABORTWARNING
!define MUI_WELCOMEPAGE_TEXT "This will install ${APP} ${VERSION} on your computer.$\r$\n$\r$\nChat with colleagues and send files and folders at full network speed, between Windows and Mac.$\r$\n$\r$\nClick Next to continue."
!define MUI_FINISHPAGE_RUN
!define MUI_FINISHPAGE_RUN_TEXT "Open ${APP} now"
!define MUI_FINISHPAGE_RUN_FUNCTION LaunchApp

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

Function .onInit
  ${If} ${RunningX64}
    SetRegView 64
  ${EndIf}
FunctionEnd

Function un.onInit
  ${If} ${RunningX64}
    SetRegView 64
  ${EndIf}
FunctionEnd

Function LaunchApp
  ; Started through Explorer so it runs as the normal user, not as admin.
  Exec '"$WINDIR\explorer.exe" "$INSTDIR\${APP}.exe"'
FunctionEnd

Section "Install"
  ; Upgrading: close the running copy so its files can be replaced.
  nsExec::Exec 'taskkill /F /IM ${APP}.exe'
  Sleep 800

  SetOutPath "$INSTDIR"
  ${If} ${IsNativeARM64}
    File "/oname=${APP}.exe" "${SRC}\${APP}-windows-arm64.exe"
  ${Else}
    File "/oname=${APP}.exe" "${SRC}\${APP}-windows-amd64.exe"
  ${EndIf}
  WriteUninstaller "$INSTDIR\Uninstall ${APP}.exe"
  ; Let OfficeChat update itself without an admin prompt (users may modify
  ; this folder; the app runs as the normal user anyway).
  nsExec::Exec 'icacls "$INSTDIR" /grant *S-1-5-32-545:(OI)(CI)M /T /Q'
  Delete "$INSTDIR\${APP}.exe.old"

  SetShellVarContext all
  CreateShortcut "$SMPROGRAMS\${APP}.lnk" "$INSTDIR\${APP}.exe" "" "$INSTDIR\${APP}.exe" 0
  CreateShortcut "$DESKTOP\${APP}.lnk" "$INSTDIR\${APP}.exe" "" "$INSTDIR\${APP}.exe" 0

  ; Let other computers reach this one on every network type (offices are
  ; often classified as "Public").
  nsExec::Exec 'netsh advfirewall firewall delete rule name="${APP}"'
  nsExec::Exec 'netsh advfirewall firewall add rule name="${APP}" dir=in action=allow program="$INSTDIR\${APP}.exe" enable=yes profile=any'
  nsExec::Exec 'netsh advfirewall firewall add rule name="${APP}" dir=in action=allow protocol=UDP localport=45454 profile=any'
  nsExec::Exec 'netsh advfirewall firewall add rule name="${APP}" dir=in action=allow protocol=TCP localport=45456-45475 profile=any'

  WriteRegStr HKLM "Software\${APP}" "InstallDir" "$INSTDIR"
  WriteRegStr HKLM "${UNKEY}" "DisplayName" "${APP}"
  WriteRegStr HKLM "${UNKEY}" "DisplayIcon" "$INSTDIR\${APP}.exe"
  WriteRegStr HKLM "${UNKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKLM "${UNKEY}" "Publisher" "${APP}"
  WriteRegStr HKLM "${UNKEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKLM "${UNKEY}" "UninstallString" '"$INSTDIR\Uninstall ${APP}.exe"'
  WriteRegStr HKLM "${UNKEY}" "QuietUninstallString" '"$INSTDIR\Uninstall ${APP}.exe" /S'
  WriteRegDWORD HKLM "${UNKEY}" "NoModify" 1
  WriteRegDWORD HKLM "${UNKEY}" "NoRepair" 1
  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  IntFmt $0 "0x%08X" $0
  WriteRegDWORD HKLM "${UNKEY}" "EstimatedSize" "$0"
SectionEnd

Section "Uninstall"
  nsExec::Exec 'taskkill /F /IM ${APP}.exe'
  Sleep 800
  SetShellVarContext all
  Delete "$SMPROGRAMS\${APP}.lnk"
  Delete "$DESKTOP\${APP}.lnk"
  Delete "$INSTDIR\${APP}.exe"
  Delete "$INSTDIR\${APP}.exe.old"
  Delete "$INSTDIR\Uninstall ${APP}.exe"
  RMDir "$INSTDIR"
  nsExec::Exec 'netsh advfirewall firewall delete rule name="${APP}"'
  DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "${APP}"
  DeleteRegKey HKLM "${UNKEY}"
  DeleteRegKey HKLM "Software\${APP}"
  ; Chat history and received files are kept (in AppData and Downloads).
SectionEnd
