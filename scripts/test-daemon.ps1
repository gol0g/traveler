# Test daemon wake (Run as Administrator)
Write-Host ""
Write-Host "=== Daemon Wake Test ===" -ForegroundColor Cyan

$WakeTime = (Get-Date).AddMinutes(2)
Write-Host "Wake time: $($WakeTime.ToString('HH:mm:ss'))"
Write-Host ""

# Power settings
powercfg /setacvalueindex SCHEME_CURRENT SUB_SLEEP BD3B718A-0680-4D9D-8AB2-E1D2B4AC806D 1
powercfg /setdcvalueindex SCHEME_CURRENT SUB_SLEEP BD3B718A-0680-4D9D-8AB2-E1D2B4AC806D 1
powercfg /setactive SCHEME_CURRENT

# Remove old tasks
Unregister-ScheduledTask -TaskName "TravelerWake" -Confirm:$false -ErrorAction SilentlyContinue
Unregister-ScheduledTask -TaskName "TravelerRun" -Confirm:$false -ErrorAction SilentlyContinue

# Task 1: Wake only (exact same as user's working method)
$action1 = New-ScheduledTaskAction -Execute "cmd.exe" -Argument "/c exit"
$trigger1 = New-ScheduledTaskTrigger -Once -At $WakeTime
$settings1 = New-ScheduledTaskSettingsSet -WakeToRun -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
Register-ScheduledTask -TaskName "TravelerWake" -Action $action1 -Trigger $trigger1 -Settings $settings1 -User "SYSTEM" -RunLevel Highest -Force | Out-Null
Write-Host "TravelerWake registered (SYSTEM)" -ForegroundColor Green

# Task 2: Run traveler (same time, current user)
$action2 = New-ScheduledTaskAction -Execute "C:\Users\JungHyun\Desktop\traveler\traveler.exe" -Argument "--daemon --sleep-on-exit=false" -WorkingDirectory "C:\Users\JungHyun\Desktop\traveler"
$trigger2 = New-ScheduledTaskTrigger -Once -At $WakeTime
$settings2 = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
Register-ScheduledTask -TaskName "TravelerRun" -Action $action2 -Trigger $trigger2 -Settings $settings2 -Force | Out-Null
Write-Host "TravelerRun registered (user)" -ForegroundColor Green

Write-Host ""
Write-Host "=== Wake Timers ===" -ForegroundColor Yellow
powercfg /waketimers
Write-Host ""

Write-Host "Sleeping in 5 seconds..." -ForegroundColor Yellow
Start-Sleep -Seconds 5
rundll32.exe powrprof.dll,SetSuspendState 0,1,0
