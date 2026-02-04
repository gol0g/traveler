# Test wake timer registration
$TaskName = "TravelerTest"
$WakeTime = "14:35"

Write-Host ""
Write-Host "=== Wake Timer Test ===" -ForegroundColor Cyan
Write-Host "Setting wake time: $WakeTime"
Write-Host ""

# Remove old
Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue

# Create task with WakeToRun
$trigger = New-ScheduledTaskTrigger -Once -At $WakeTime
$action = New-ScheduledTaskAction -Execute "notepad.exe"
$settings = New-ScheduledTaskSettingsSet -WakeToRun -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
Register-ScheduledTask -TaskName $TaskName -Trigger $trigger -Action $action -Settings $settings -Force | Out-Null

Write-Host "Task registered." -ForegroundColor Green
Write-Host ""
Write-Host "Checking wake timers..." -ForegroundColor Yellow
Write-Host ""
powercfg /waketimers
Write-Host ""
pause
