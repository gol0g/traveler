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

# Remove old task
Unregister-ScheduledTask -TaskName "TravelerDaemon" -Confirm:$false -ErrorAction SilentlyContinue

# Create task - runs at 9:20 AM ET (23:20 KST) by default
# traveler.exe will update the time on each run
$defaultTime = "23:20"
$exePath = "C:\Users\JungHyun\Desktop\traveler\traveler.exe"
$workDir = "C:\Users\JungHyun\Desktop\traveler"
$dataDir = "$env:USERPROFILE\.traveler"

$trigger = New-ScheduledTaskTrigger -Daily -At $defaultTime
$action = New-ScheduledTaskAction -Execute $exePath -Argument "--daemon --data-dir `"$dataDir`"" -WorkingDirectory $workDir
$settings = New-ScheduledTaskSettingsSet -WakeToRun -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable
$principal = New-ScheduledTaskPrincipal -UserId "SYSTEM" -RunLevel Highest

Register-ScheduledTask -TaskName "TravelerDaemon" -Trigger $trigger -Action $action -Settings $settings -Principal $principal -Force | Out-Null

Write-Host ""
Write-Host "Task 'TravelerDaemon' registered!" -ForegroundColor Green
Write-Host "  - Runs daily at $defaultTime (KST)"
Write-Host "  - Wake from sleep: Yes"
Write-Host "  - Run as: SYSTEM"
Write-Host "  - Data dir: $dataDir"
Write-Host ""

# Verify
Write-Host "=== Verification ===" -ForegroundColor Yellow
schtasks /query /tn "TravelerDaemon" /v /fo list | Select-String "Task Name|Status|Next Run|Run As"
Write-Host ""
powercfg /waketimers
Write-Host ""
pause
