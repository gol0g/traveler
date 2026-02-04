# Traveler 자동화 테스트 (2분 뒤 절전 해제)
# 관리자 권한으로 실행

$TaskName = "TravelerAutoTradeTest"
$TravelerPath = "C:\Users\JungHyun\Desktop\traveler"
$ExePath = Join-Path $TravelerPath "traveler.exe"

# 2분 뒤 시간 계산
$WakeTime = (Get-Date).AddMinutes(2).ToString("HH:mm")

Write-Host ""
Write-Host "========================================" -ForegroundColor Cyan
Write-Host " Traveler 자동화 테스트" -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan
Write-Host ""
Write-Host " 현재 시간: $(Get-Date -Format 'HH:mm:ss')"
Write-Host " 절전 해제: $WakeTime (2분 뒤)"
Write-Host ""

# 기존 테스트 작업 삭제
Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue

# 트리거: 지정 시간 (1회)
$Trigger = New-ScheduledTaskTrigger -Once -At $WakeTime

# 액션: Traveler 데몬 (절전 안함 - 테스트용)
$Action = New-ScheduledTaskAction `
    -Execute $ExePath `
    -Argument "--daemon --sleep-on-exit=false" `
    -WorkingDirectory $TravelerPath

# 설정: 절전 해제!
$Settings = New-ScheduledTaskSettingsSet `
    -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries `
    -WakeToRun `
    -StartWhenAvailable

$Principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Highest

Register-ScheduledTask `
    -TaskName $TaskName `
    -Trigger $Trigger `
    -Action $Action `
    -Settings $Settings `
    -Principal $Principal `
    -Description "Traveler 자동화 테스트"

Write-Host " 스케줄 등록 완료!" -ForegroundColor Green
Write-Host ""
Write-Host " 테스트 순서:" -ForegroundColor Yellow
Write-Host " 1. 이 창 닫기"
Write-Host " 2. 바로 PC 절전 모드 진입 (시작 > 전원 > 절전)"
Write-Host " 3. 2분 뒤 자동 절전 해제 확인"
Write-Host " 4. Traveler 자동 실행 확인"
Write-Host ""
Write-Host " 절전 해제 예정: $WakeTime" -ForegroundColor Green
Write-Host ""
pause
