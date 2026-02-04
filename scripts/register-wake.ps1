# Register wake timer + traveler task
$WakeTime = (Get-Date).AddMinutes(2)

Write-Host ""
Write-Host "=== Register Wake Timer ===" -ForegroundColor Cyan
Write-Host "Wake at: $($WakeTime.ToString('HH:mm:ss'))"

# Power setting = 1
powercfg /setacvalueindex SCHEME_CURRENT SUB_SLEEP BD3B718A-0680-4D9D-8AB2-E1D2B4AC806D 1
powercfg /setdcvalueindex SCHEME_CURRENT SUB_SLEEP BD3B718A-0680-4D9D-8AB2-E1D2B4AC806D 1
powercfg /setactive SCHEME_CURRENT

# Remove old tasks
Unregister-ScheduledTask -TaskName "WakeTest" -Confirm:$false -ErrorAction SilentlyContinue
Unregister-ScheduledTask -TaskName "TravelerRun" -Confirm:$false -ErrorAction SilentlyContinue

# Task 1: Wake only (SYSTEM)
$trigger1 = New-ScheduledTaskTrigger -Once -At $WakeTime
$action1 = New-ScheduledTaskAction -Execute "cmd.exe" -Argument "/c exit"
$settings1 = New-ScheduledTaskSettingsSet -WakeToRun -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
Register-ScheduledTask -TaskName "WakeTest" -Trigger $trigger1 -Action $action1 -Settings $settings1 -User "SYSTEM" -RunLevel Highest -Force | Out-Null
Write-Host "WakeTest task registered (SYSTEM)" -ForegroundColor Green

# Task 2: Run traveler (current user)
$trigger2 = New-ScheduledTaskTrigger -Once -At $WakeTime
$action2 = New-ScheduledTaskAction -Execute "C:\Users\JungHyun\Desktop\traveler\traveler.exe" -Argument "--daemon --sleep-on-exit=false" -WorkingDirectory "C:\Users\JungHyun\Desktop\traveler"
$settings2 = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
Register-ScheduledTask -TaskName "TravelerRun" -Trigger $trigger2 -Action $action2 -Settings $settings2 -Force | Out-Null
Write-Host "TravelerRun task registered (user)" -ForegroundColor Green

Write-Host ""
Write-Host "=== Wake Timers ===" -ForegroundColor Yellow
powercfg /waketimers
Write-Host ""
pause
