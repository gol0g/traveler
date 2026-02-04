# Traveler 완전 자동화 설정
# 관리자 권한으로 실행: powershell -ExecutionPolicy Bypass -File setup-full-automation.ps1

param(
    [string]$TravelerPath = "C:\Users\JungHyun\Desktop\traveler",
    [string]$WakeTime = "22:00"  # KST 기준 (미장 시작 30분 전)
)

$TaskName = "TravelerAutoTrade"
$ExePath = Join-Path $TravelerPath "traveler.exe"
$ConfigPath = Join-Path $TravelerPath "config.yaml"

Write-Host ""
Write-Host "========================================" -ForegroundColor Cyan
Write-Host " Traveler 완전 자동화 설정" -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan
Write-Host ""

# 1. 기존 작업 삭제
Write-Host "[1/3] 기존 스케줄 작업 정리..." -ForegroundColor Yellow
Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue

# 2. 새 작업 생성
Write-Host "[2/3] 스케줄 작업 생성..." -ForegroundColor Yellow

# 트리거: 매일 지정 시간 + 절전 해제
$Trigger = New-ScheduledTaskTrigger -Daily -At $WakeTime

# 액션: Traveler 데몬 모드
$Action = New-ScheduledTaskAction `
    -Execute $ExePath `
    -Argument "--daemon --config `"$ConfigPath`"" `
    -WorkingDirectory $TravelerPath

# 설정
$Settings = New-ScheduledTaskSettingsSet `
    -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries `
    -WakeToRun `
    -StartWhenAvailable `
    -ExecutionTimeLimit (New-TimeSpan -Hours 12) `
    -RestartCount 3 `
    -RestartInterval (New-TimeSpan -Minutes 1)

# 등록 (현재 사용자, 최고 권한)
$Principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Highest

Register-ScheduledTask `
    -TaskName $TaskName `
    -Trigger $Trigger `
    -Action $Action `
    -Settings $Settings `
    -Principal $Principal `
    -Description "Traveler 미국 주식 자동 매매 (매일 $WakeTime KST)"

# 3. 확인
Write-Host "[3/3] 설정 확인..." -ForegroundColor Yellow
$Task = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue

if ($Task) {
    Write-Host ""
    Write-Host "========================================" -ForegroundColor Green
    Write-Host " 설정 완료!" -ForegroundColor Green
    Write-Host "========================================" -ForegroundColor Green
    Write-Host ""
    Write-Host " Task Name:     $TaskName"
    Write-Host " Wake Time:     $WakeTime (KST)"
    Write-Host " Executable:    $ExePath"
    Write-Host " Wake from Sleep: YES"
    Write-Host ""
    Write-Host " 다음 단계:" -ForegroundColor Yellow
    Write-Host " 1. scripts\setup-autologin.bat 실행 (자동 로그인 설정)"
    Write-Host " 2. PC 재시작하여 자동 로그인 확인"
    Write-Host " 3. 오늘 밤 $WakeTime 에 자동 실행 확인"
    Write-Host ""
    Write-Host " 수동 테스트:" -ForegroundColor Yellow
    Write-Host " schtasks /run /tn `"$TaskName`""
    Write-Host ""
} else {
    Write-Host ""
    Write-Host " 설정 실패! 관리자 권한으로 다시 실행하세요." -ForegroundColor Red
    Write-Host ""
}
