# Traveler 자동 매매 스케줄러 설정
# 관리자 권한으로 실행 필요

param(
    [string]$TravelerPath = "C:\Users\JungHyun\Desktop\traveler\traveler.exe",
    [string]$ConfigPath = "C:\Users\JungHyun\Desktop\traveler\config.yaml",
    [string]$WakeTime = "22:00"  # KST 기준 (미장 시작 30분 전)
)

$TaskName = "TravelerAutoTrade"

# 기존 작업 삭제
Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue

# 트리거: 매일 지정된 시간 (절전 해제 포함)
$Trigger = New-ScheduledTaskTrigger -Daily -At $WakeTime

# 액션: Traveler 데몬 모드 실행
$Action = New-ScheduledTaskAction -Execute $TravelerPath -Argument "--daemon --config `"$ConfigPath`"" -WorkingDirectory (Split-Path $TravelerPath)

# 설정: 절전 해제, 배터리 무관
$Settings = New-ScheduledTaskSettingsSet `
    -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries `
    -WakeToRun `
    -StartWhenAvailable `
    -ExecutionTimeLimit (New-TimeSpan -Hours 12)

# 작업 등록
Register-ScheduledTask `
    -TaskName $TaskName `
    -Trigger $Trigger `
    -Action $Action `
    -Settings $Settings `
    -Description "Traveler 미국 주식 자동 매매 데몬" `
    -User $env:USERNAME `
    -RunLevel Highest

Write-Host ""
Write-Host "========================================" -ForegroundColor Green
Write-Host " Traveler 스케줄러 설정 완료" -ForegroundColor Green
Write-Host "========================================" -ForegroundColor Green
Write-Host ""
Write-Host " Task Name:  $TaskName"
Write-Host " Wake Time:  $WakeTime (KST)"
Write-Host " Executable: $TravelerPath"
Write-Host ""
Write-Host " 확인: Task Scheduler에서 '$TaskName' 확인"
Write-Host " 테스트: schtasks /run /tn '$TaskName'"
Write-Host ""
