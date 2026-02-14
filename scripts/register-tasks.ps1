# Register scheduled tasks (no pause, no interactive)
powercfg /setacvalueindex SCHEME_CURRENT SUB_SLEEP BD3B718A-0680-4D9D-8AB2-E1D2B4AC806D 1
powercfg /setdcvalueindex SCHEME_CURRENT SUB_SLEEP BD3B718A-0680-4D9D-8AB2-E1D2B4AC806D 1
powercfg /setactive SCHEME_CURRENT

$exePath = "C:\Users\JungHyun\Desktop\traveler\traveler.exe"
$workDir = "C:\Users\JungHyun\Desktop\traveler"
$dataDir = "C:\Users\JungHyun\.traveler"

# --- Web server (always on) ---
Unregister-ScheduledTask -TaskName "TravelerWeb" -Confirm:$false -ErrorAction SilentlyContinue
$tWeb = New-ScheduledTaskTrigger -AtLogOn
$aWeb = New-ScheduledTaskAction -Execute $exePath -Argument "--web --data-dir `"$dataDir`"" -WorkingDirectory $workDir
$sWeb = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit (New-TimeSpan -Days 365)
Register-ScheduledTask -TaskName "TravelerWeb" -Trigger $tWeb -Action $aWeb -Settings $sWeb -RunLevel Highest -Force | Out-Null

# --- US daemon (daily 23:20 KST) ---
Unregister-ScheduledTask -TaskName "TravelerDaemon" -Confirm:$false -ErrorAction SilentlyContinue
$t1 = New-ScheduledTaskTrigger -Daily -At "23:20"
$a1 = New-ScheduledTaskAction -Execute $exePath -Argument "--daemon --market us --data-dir `"$dataDir`"" -WorkingDirectory $workDir
$s1 = New-ScheduledTaskSettingsSet -WakeToRun -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable
Register-ScheduledTask -TaskName "TravelerDaemon" -Trigger $t1 -Action $a1 -Settings $s1 -RunLevel Highest -Force | Out-Null

# --- KR daemon (daily 08:40 KST) ---
Unregister-ScheduledTask -TaskName "TravelerDaemonKR" -Confirm:$false -ErrorAction SilentlyContinue
$t2 = New-ScheduledTaskTrigger -Daily -At "08:40"
$a2 = New-ScheduledTaskAction -Execute $exePath -Argument "--daemon --market kr --data-dir `"$dataDir`"" -WorkingDirectory $workDir
$s2 = New-ScheduledTaskSettingsSet -WakeToRun -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable
Register-ScheduledTask -TaskName "TravelerDaemonKR" -Trigger $t2 -Action $a2 -Settings $s2 -RunLevel Highest -Force | Out-Null

Write-Host "DONE - All tasks registered (Web=AtLogon, US=23:20, KR=08:40)"
