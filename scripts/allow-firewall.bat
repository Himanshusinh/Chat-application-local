@echo off
rem Allows OfficeChat through Windows Firewall on all network types
rem (Private AND Public - offices are often marked Public by mistake).
net session >nul 2>&1
if %errorlevel% neq 0 (
  powershell -NoProfile -Command "Start-Process -FilePath '%~f0' -Verb RunAs"
  exit /b
)
netsh advfirewall firewall delete rule name="OfficeChat" >nul 2>&1
netsh advfirewall firewall add rule name="OfficeChat" dir=in action=allow program="%~dp0OfficeChat.exe" enable=yes profile=any
netsh advfirewall firewall add rule name="OfficeChat" dir=in action=allow protocol=UDP localport=45454 profile=any
netsh advfirewall firewall add rule name="OfficeChat" dir=in action=allow protocol=TCP localport=45456-45475 profile=any
echo.
echo OfficeChat is now allowed through Windows Firewall.
pause
