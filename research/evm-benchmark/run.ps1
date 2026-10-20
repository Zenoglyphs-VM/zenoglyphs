param([int[]]$Steps = @(4, 8, 16, 32, 64), [string]$Duration = '4m', [int]$PlasmaQsr = 100, [string]$Fund = '0.5')
$d = $PSScriptRoot
$exe = "$env:TEMP\zcribe-swarm.exe"
Remove-Item "$d\stop" -ErrorAction SilentlyContinue
$sampler = Start-Process pwsh -ArgumentList '-NoProfile', '-File', "$d\sample.ps1", '-Out', "$d\env-$($Steps[0]).csv", '-Every', '10', '-StopFile', "$d\stop" -PassThru -WindowStyle Hidden
Set-Location C:\dev\zenon\projects\zenoglyphs
# one idle minute first, as the baseline every step is compared against
"$(Get-Date -Format o) idle baseline" | Add-Content "$d\progress.txt"
Start-Sleep 60
foreach ($n in $Steps) {
  "$(Get-Date -Format o) step $n wallets start" | Add-Content "$d\progress.txt"
  & $exe evm swarm -devnet -swarm-index 100 -wallets $n -duration $Duration -plasma-qsr $PlasmaQsr -fund $Fund -report "$d\step-$n.json" *> "$d\step-$n.log"
  "$(Get-Date -Format o) step $n wallets exit $LASTEXITCODE" | Add-Content "$d\progress.txt"
  Start-Sleep 30  # let the indexer and price settle between steps
}
New-Item "$d\stop" | Out-Null
Wait-Process -Id $sampler.Id -Timeout 60 -ErrorAction SilentlyContinue
"$(Get-Date -Format o) done" | Add-Content "$d\progress.txt"
