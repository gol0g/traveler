# Initial setup for Traveler daemon (Run as Administrator ONCE)
Write-Host ""
Write-Host "=== Traveler Daemon Setup ===" -ForegroundColor Cyan
Write-Host "This script registers the wake timer task."
Write-Host "Run this ONCE as Administrator."
Write-Host ""

# Power settings for wake timer
Write-Host "Setting power config..." -ForegroundColor Yellow
powercfg /setacvalueindex SCHEME_CURRENT SUB_SLEEP BD3B718A-0680-4D9D-8AB2-E1D2B4AC806D 1
powercfg /setdcvalueindex SCHEME_CURRENT SUB_SLEEP BD3B718A-0680-4D9D-8AB2-E1D2B4AC806D 1
powercfg /setactive SCHEME_CURRENT

$exePath = "C:\Users\JungHyun\Desktop\traveler\traveler.exe"
$workDir = "C:\Users\JungHyun\Desktop\traveler"
$dataDir = "$env:USERPROFILE\.traveler"

# --- US Market (TravelerDaemon) ---
Write-Host "[US] Registering TravelerDaemon..." -ForegroundColor Yellow
Unregister-ScheduledTask -TaskName "TravelerDaemon" -Confirm:$false -ErrorAction SilentlyContinue

# 23:20 KST = 9:20 AM ET (미국장 개장 10분 전)
$usTime = "23:20"
$usTrigger = New-ScheduledTaskTrigger -Daily -At $usTime
$usAction = New-ScheduledTaskAction -Execute $exePath -Argument "--daemon --market us --data-dir `"$dataDir`"" -WorkingDirectory $workDir
$usSettings = New-ScheduledTaskSettingsSet -WakeToRun -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable
$usPrincipal = New-ScheduledTaskPrincipal -UserId "SYSTEM" -RunLevel Highest

Register-ScheduledTask -TaskName "TravelerDaemon" -Trigger $usTrigger -Action $usAction -Settings $usSettings -Principal $usPrincipal -Force | Out-Null

Write-Host "  Task 'TravelerDaemon' registered!" -ForegroundColor Green
Write-Host "  - Runs daily at $usTime (KST) = 9:20 AM ET"
Write-Host "  - Market: US (--market us)"
Write-Host ""

# --- KR Market (TravelerDaemonKR) ---
Write-Host "[KR] Registering TravelerDaemonKR..." -ForegroundColor Yellow
Unregister-ScheduledTask -TaskName "TravelerDaemonKR" -Confirm:$false -ErrorAction SilentlyContinue

# 08:40 KST (한국장 개장 20분 전)
$krTime = "08:40"
$krTrigger = New-ScheduledTaskTrigger -Daily -At $krTime
$krAction = New-ScheduledTaskAction -Execute $exePath -Argument "--daemon --market kr --data-dir `"$dataDir`"" -WorkingDirectory $workDir
$krSettings = New-ScheduledTaskSettingsSet -WakeToRun -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable
$krPrincipal = New-ScheduledTaskPrincipal -UserId "SYSTEM" -RunLevel Highest

Register-ScheduledTask -TaskName "TravelerDaemonKR" -Trigger $krTrigger -Action $krAction -Settings $krSettings -Principal $krPrincipal -Force | Out-Null

Write-Host "  Task 'TravelerDaemonKR' registered!" -ForegroundColor Green
Write-Host "  - Runs daily at $krTime (KST)"
Write-Host "  - Market: KR (--market kr)"
Write-Host ""

# --- Verification ---
Write-Host "=== Verification ===" -ForegroundColor Yellow
Write-Host ""
Write-Host "[US] TravelerDaemon:" -ForegroundColor Cyan
schtasks /query /tn "TravelerDaemon" /v /fo list | Select-String "Task Name|Status|Next Run|Run As"
Write-Host ""
Write-Host "[KR] TravelerDaemonKR:" -ForegroundColor Cyan
schtasks /query /tn "TravelerDaemonKR" /v /fo list | Select-String "Task Name|Status|Next Run|Run As"
Write-Host ""
powercfg /waketimers
Write-Host ""
pause
