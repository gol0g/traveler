# Stop all traveler tasks, rebuild, restart all that were running
$logFile = "C:\Users\JungHyun\Desktop\traveler\scripts\rebuild-result.txt"
try {
    # Remember which tasks were running before rebuild
    $tasks = @('TravelerWeb', 'TravelerDaemon', 'TravelerDaemonKR')
    $wasRunning = @()
    foreach ($name in $tasks) {
        $task = Get-ScheduledTask -TaskName $name -ErrorAction SilentlyContinue
        if ($task -and $task.State -eq 'Running') {
            $wasRunning += $name
        }
    }
    "RUNNING BEFORE: $($wasRunning -join ', ')" | Out-File $logFile

    # Stop all tasks
    foreach ($name in $tasks) {
        Stop-ScheduledTask -TaskName $name -ErrorAction SilentlyContinue
    }
    Start-Sleep -Seconds 2

    # Kill any remaining processes
    Get-Process traveler -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 2

    # Build
    Set-Location "C:\Users\JungHyun\Desktop\traveler"
    $buildOutput = & go build -o traveler.exe ./cmd/traveler 2>&1
    if ($LASTEXITCODE -ne 0) {
        "BUILD FAILED: $buildOutput" | Out-File $logFile -Append
        exit 1
    }
    "BUILD OK" | Out-File $logFile -Append

    # Restart all tasks that were running + always restart TravelerWeb
    if ($wasRunning -notcontains 'TravelerWeb') {
        $wasRunning += 'TravelerWeb'
    }
    foreach ($name in $wasRunning) {
        Start-ScheduledTask -TaskName $name -ErrorAction SilentlyContinue
        "RESTARTED: $name" | Out-File $logFile -Append
    }
    Start-Sleep -Seconds 3

    # Report final state
    Get-ScheduledTask -TaskName 'Traveler*' | Select-Object TaskName, State | Out-String | Out-File $logFile -Append

    # Verify web is up
    $resp = Invoke-WebRequest -Uri "http://localhost:8080/api/scan/status" -UseBasicParsing -ErrorAction SilentlyContinue
    "WEB STATUS: $($resp.StatusCode)" | Out-File $logFile -Append
} catch {
    "ERROR: $($_.Exception.Message)" | Out-File $logFile -Append
}
