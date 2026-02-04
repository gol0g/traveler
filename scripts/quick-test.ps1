# Wake test - 15:53
$TaskName = "TravelerTest"
$WakeTime = "15:53"

Write-Host ""
Write-Host "=== Wake Test ===" -ForegroundColor Cyan
Write-Host "Wake at: $WakeTime"
Write-Host ""

Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue

$trigger = New-ScheduledTaskTrigger -Once -At $WakeTime
$action = New-ScheduledTaskAction -Execute "C:\Users\JungHyun\Desktop\traveler\traveler.exe" -Argument "--daemon --sleep-on-exit=false" -WorkingDirectory "C:\Users\JungHyun\Desktop\traveler"
$settings = New-ScheduledTaskSettingsSet -WakeToRun -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries

Register-ScheduledTask -TaskName $TaskName -Trigger $trigger -Action $action -Settings $settings -Force | Out-Null

Write-Host "Task registered" -ForegroundColor Green
powercfg /waketimers
Write-Host ""
Write-Host "Sleeping in 3 seconds..." -ForegroundColor Yellow
Start-Sleep -Seconds 3
rundll32.exe powrprof.dll,SetSuspendState 0,1,0
