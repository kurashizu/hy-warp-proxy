# Remove the hygw gateway (Windows). VMs attached to it simply lose their network.
$dest = if ($env:HYGW_HOME) { $env:HYGW_HOME } else { Join-Path $env:LOCALAPPDATA 'hygw' }
$taskName = 'hygw-gateway'
if (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue) {
  Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
  Unregister-ScheduledTask -TaskName $taskName -Confirm:$false
}
Get-Process -Name hygw, speederv2 -ErrorAction SilentlyContinue | Where-Object { $_.Path -like "$dest*" } | Stop-Process -Force -ErrorAction SilentlyContinue
Get-CimInstance Win32_Process -Filter "Name='powershell.exe'" | Where-Object { $_.CommandLine -like "*$dest*run.ps1*" } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
Start-Sleep -Seconds 1
Remove-Item -Recurse -Force $dest -ErrorAction SilentlyContinue
Write-Host "removed $dest and the scheduled task"
