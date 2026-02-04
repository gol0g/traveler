# Wake + Traveler test
powercfg /setacvalueindex SCHEME_CURRENT SUB_SLEEP BD3B718A-0680-4D9D-8AB2-E1D2B4AC806D 1
powercfg /setdcvalueindex SCHEME_CURRENT SUB_SLEEP BD3B718A-0680-4D9D-8AB2-E1D2B4AC806D 1
powercfg /setactive SCHEME_CURRENT

# 모니터 타임아웃 0 (모니터 끄기 비활성화)
powercfg /change monitor-timeout-ac 0
powercfg /change monitor-timeout-dc 0

Unregister-ScheduledTask -TaskName "WakeTest" -Confirm:$false -ErrorAction SilentlyContinue
Unregister-ScheduledTask -TaskName "TravelerRun" -Confirm:$false -ErrorAction SilentlyContinue

$WakeTime = (Get-Date).AddMinutes(2)
Write-Host "Wake at: $($WakeTime.ToString('HH:mm:ss'))" -ForegroundColor Cyan

# Task 1: Wake (SYSTEM)
$action1 = New-ScheduledTaskAction -Execute "cmd.exe" -Argument "/c exit"
$trigger1 = New-ScheduledTaskTrigger -Once -At $WakeTime
$settings1 = New-ScheduledTaskSettingsSet -WakeToRun -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
Register-ScheduledTask -TaskName "WakeTest" -Action $action1 -Trigger $trigger1 -Settings $settings1 -User "SYSTEM" -RunLevel Highest -Force

# Task 2: Traveler
$action2 = New-ScheduledTaskAction -Execute "C:\Users\JungHyun\Desktop\traveler\traveler.exe" -Argument "--daemon --sleep-on-exit=false" -WorkingDirectory "C:\Users\JungHyun\Desktop\traveler"
$trigger2 = New-ScheduledTaskTrigger -Once -At $WakeTime
$settings2 = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
Register-ScheduledTask -TaskName "TravelerRun" -Action $action2 -Trigger $trigger2 -Settings $settings2 -Force

Write-Host ""
powercfg /waketimers
Write-Host ""
Write-Host "Sleeping in 3 seconds..." -ForegroundColor Yellow
Start-Sleep -Seconds 3

Add-Type -Assembly System.Windows.Forms
[System.Windows.Forms.Application]::SetSuspendState("Suspend", $false, $false)
