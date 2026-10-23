# Real-Windows smoke test, run by CI on windows-latest (and usable by hand).
# Proves the wake request really reaches Windows: it must appear in `powercfg /requests`
# while held and disappear after release.
$ErrorActionPreference = 'Stop'

$root = Resolve-Path (Join-Path $PSScriptRoot '..')
Push-Location $root
try {
  go build -o agentawake.exe ./cmd/agentawake
  $exe = Join-Path $root 'agentawake.exe'

  $env:AGENTAWAKE_HOME = Join-Path ([System.IO.Path]::GetTempPath()) ("aa-smoke-" + [guid]::NewGuid())
  $env:AGENTAWAKE_CONFIG = Join-Path $env:AGENTAWAKE_HOME 'config.json'
  New-Item -ItemType Directory -Force $env:AGENTAWAKE_HOME | Out-Null
  '{"poll_seconds":1,"process_detection":false}' | Set-Content $env:AGENTAWAKE_CONFIG

  & $exe doctor
  & $exe start
  if ($LASTEXITCODE -ne 0) { throw 'daemon did not start' }

  & $exe hold 5m | Out-Null
  Start-Sleep -Seconds 4
  $held = (powercfg /requests) -join "`n"
  Write-Host "---- powercfg /requests (held) ----`n$held"
  if ($held -notmatch 'agentawake') { throw 'FAIL: wake request not visible in powercfg /requests while holding' }

  & $exe status

  & $exe release | Out-Null
  Start-Sleep -Seconds 4
  $released = (powercfg /requests) -join "`n"
  if ($released -match 'agentawake') { throw 'FAIL: wake request still present after release' }

  & $exe stop
  Write-Host 'PASS: wake request appears while held and clears on release'
}
finally {
  Pop-Location
}
