# Install the hygw gateway on a Windows HOST (per-user; no admin needed).
#   powershell -ExecutionPolicy Bypass -File .\install.ps1 [-SettingsFrom <file from the server: hy-client-settings.env>]
# Env: $env:HYGW_HOME overrides the install dir (default %LOCALAPPDATA%\hygw)
param([string]$SettingsFrom = '')
$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot

function Say($m) { Write-Host "`n==> $m" -ForegroundColor Green }

$arch = switch ($env:PROCESSOR_ARCHITECTURE) { 'AMD64' { 'amd64' } 'ARM64' { 'arm64' } default { throw "unsupported CPU: $env:PROCESSOR_ARCHITECTURE" } }
$src = "bin\hygw-windows-$arch.exe"
if (-not (Test-Path $src)) { throw "no binary $src" }
if (-not (Test-Path settings.env)) { throw 'settings.env is missing' }
if ($SettingsFrom) {
  if (-not (Test-Path $SettingsFrom)) { throw "-SettingsFrom file not found: $SettingsFrom" }
  $lines = Get-Content settings.env
  foreach ($k in 'SERVER_HOST','SERVER_PORT','AUTH_PASSWORD','CERT_PIN','SNI','FEC_PORT','SPEEDER_KEY') {
    $m = Get-Content $SettingsFrom | Where-Object { $_ -match "^$k=" } | Select-Object -Last 1
    if (-not $m) { continue }
    if ($lines -match "^$k=") { $lines = $lines -replace "^$k=.*", $m } else { $lines += $m }
  }
  Set-Content -Path settings.env -Value $lines -Encoding ascii
  Write-Host "merged server settings from $SettingsFrom"
}

$dest = if ($env:HYGW_HOME) { $env:HYGW_HOME } else { Join-Path $env:LOCALAPPDATA 'hygw' }
Say "Installing to $dest (windows-$arch)"
New-Item -ItemType Directory -Force -Path $dest | Out-Null

# stop a running instance
$taskName = 'hygw-gateway'
if (Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue) {
  Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
}
Get-Process -Name hygw, speederv2_wepoll -ErrorAction SilentlyContinue | Where-Object { $_.Path -like "$dest*" } | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 500

Copy-Item $src (Join-Path $dest 'hygw.exe') -Force
Copy-Item settings.env (Join-Path $dest 'settings.env') -Force
# restrict the settings file (it holds the password) to the current user
icacls (Join-Path $dest 'settings.env') /inheritance:r /grant:r "$($env:USERNAME):(R,W)" | Out-Null

function Set-Kv($key, $val) {
  $f = Join-Path $dest 'settings.env'
  $lines = Get-Content $f
  if ($lines -match "^$key=") { $lines = $lines -replace "^$key=.*", "$key=$val" } else { $lines += "$key=$val" }
  Set-Content -Path $f -Value $lines -Encoding ascii
}
Set-Kv 'LOG_FILE' (Join-Path $dest 'hygw.log')
Set-Kv 'STATUS_FILE' (Join-Path $dest 'hygw.status.json')

$fec = ((Get-Content (Join-Path $dest 'settings.env')) | Where-Object { $_ -match '^FEC=' } | Select-Object -Last 1) -replace 'FEC=', ''
if ($fec -eq '1') {
  if ($arch -ne 'amd64') { throw 'FEC (UDPspeeder) has no ARM64 Windows binary. Set FEC=0 in settings.env and re-run.' }
  Copy-Item 'bin\speederv2_wepoll.exe' (Join-Path $dest 'speederv2.exe') -Force
  Set-Kv 'SPEEDER_BIN' (Join-Path $dest 'speederv2.exe')
}

$exe = Join-Path $dest 'hygw.exe'
$cfg = Join-Path $dest 'settings.env'
Say 'Validating settings'
& $exe -config $cfg -qemu-args | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'settings.env is invalid (see message above)' }

# a tiny supervisor loop so the gateway restarts if it ever exits (VMs are offline meanwhile)
$run = Join-Path $dest 'run.ps1'
@"
while (`$true) {
  & '$exe' -config '$cfg'
  Start-Sleep -Seconds 2
}
"@ | Set-Content -Path $run -Encoding ascii

Say 'Registering scheduled task (runs hidden at logon)'
$action = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument "-NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -File `"$run`""
$trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew
Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Settings $settings -Force | Out-Null
Start-ScheduledTask -TaskName $taskName

Say 'Waiting for the tunnel and the WARP exit check (up to 45s)'
$ok = $false
for ($i = 0; $i -lt 23; $i++) {
  Start-Sleep -Seconds 2
  & $exe -config $cfg -status | Out-Null
  if ($LASTEXITCODE -eq 0) { $ok = $true; break }
}
& $exe -config $cfg -status
if (-not $ok) {
  Write-Host "`nThe gateway is installed and running, but the exit is NOT proven to be WARP yet."
  Write-Host "That is the safe state: a VM attached to it has no network. See the reason above and $dest\hygw.log"
  exit 3
}
Say 'READY. Gateway is up and the exit is Cloudflare WARP.'
Write-Host 'Attach VMs with these QEMU network options (and NO other network device):'
& $exe -config $cfg -qemu-args
Write-Host "`nCheck any time:  $exe -config $cfg -status   (exit 0 = WARP ok, 3 = gate closed, 1 = not running)"
