# Set auto-login password
Write-Host "=== Auto-Login Password Setup ===" -ForegroundColor Cyan
Write-Host ""
$pw = Read-Host "Enter your Windows password"
Set-ItemProperty -Path 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon' -Name DefaultPassword -Value $pw
Write-Host ""
Write-Host "Password saved!" -ForegroundColor Green
Write-Host ""

# Verify
$check = Get-ItemProperty -Path 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon' -Name DefaultPassword
if ($check.DefaultPassword) {
    Write-Host "Verified: Password is set" -ForegroundColor Green
} else {
    Write-Host "Error: Password not saved" -ForegroundColor Red
}
pause
