$tasks = @("Auto Wake", "TravelerDaemon", "TravelerDaemonKR", "TravelerRun", "TravelerWeb")
foreach ($t in $tasks) {
    try {
        Unregister-ScheduledTask -TaskName $t -Confirm:$false -ErrorAction Stop
        Write-Host "Deleted: $t"
    } catch {
        Write-Host "Failed: $t - $($_.Exception.Message)"
    }
}
# Kill any remaining traveler processes
Get-Process -Name "traveler*" -ErrorAction SilentlyContinue | Stop-Process -Force
Write-Host "`n--- Remaining tasks ---"
Get-ScheduledTask | Where-Object { $_.TaskName -like "*raveler*" -or $_.TaskName -like "*ake*" } | Format-Table TaskName, State -AutoSize
