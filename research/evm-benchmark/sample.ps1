param([string]$Out, [int]$Every = 10, [string]$StopFile)
# Samples the environment around a swarm run into a CSV, one row per $Every seconds,
# until $StopFile exists.
$db = 'C:\dev\zenon\projects\zenoglyphs\zenoglyphs.db'
$containers = 'znnd-devnet-rpc','znnd-devnet-pillar','znnd-devnet-pillar2','znnd-devnet-pillar3','znnd-devnet-observer'
function rpc($m, $p) {
  (Invoke-RestMethod -Method Post -Uri http://localhost:35997 -ContentType 'application/json' -TimeoutSec 10 `
    -Body (@{jsonrpc='2.0';id=1;method=$m;params=$p} | ConvertTo-Json -Compress -Depth 5)).result
}
function timed($url) {
  $sw = [Diagnostics.Stopwatch]::StartNew()
  try { $r = Invoke-RestMethod $url -TimeoutSec 30 } catch { $r = $null }
  @{ r = $r; ms = $sw.ElapsedMilliseconds }
}
$first = $true
while (-not (Test-Path $StopFile)) {
  $t0 = Get-Date
  $row = [ordered]@{ ts = [DateTimeOffset]::Now.ToUnixTimeMilliseconds() }
  foreach ($c in $containers) {
    $du = docker exec $c du -sb /root/.znn 2>$null
    $row["disk_$($c -replace 'znnd-devnet-','')"] = if ($du) { [int64]($du -split '\s+')[0] } else { '' }
  }
  $row.db_bytes  = (Get-Item $db).Length
  $row.wal_bytes = (Get-Item "$db-wal" -ErrorAction SilentlyContinue).Length
  $p = Get-Process -Name zcribe -ErrorAction SilentlyContinue | Where-Object { $_.Path -like 'C:\dev\zenon\projects\zenoglyphs\zcribe.exe' } | Select-Object -First 1
  $row.serve_rss = if ($p) { $p.WorkingSet64 } else { '' }
  $row.serve_cpu_s = if ($p) { [math]::Round($p.TotalProcessorTime.TotalSeconds, 2) } else { '' }
  $i = timed 'http://127.0.0.1:8710/info'
  $row.info_ms = $i.ms; $row.sync_height = $i.r.syncHeight; $row.node_height = $i.r.nodeHeight
  $row.events = $i.r.events; $row.content_bytes = $i.r.contentBytes; $row.db_size_api = $i.r.dbSizeBytes
  $o = timed 'http://127.0.0.1:8710/evm/overview'
  $row.overview_ms = $o.ms; $row.evm_txs = $o.r.txs; $row.evm_failed = $o.r.failed
  $row.evm_rebuild_ms = $o.r.rebuildMs; $row.evm_content_bytes = $o.r.contentBytes; $row.evm_accounts = $o.r.accounts
  try {
    $m = rpc 'ledger.getFrontierMomentum' @()
    $row.momentum = $m.height; $row.momentum_ts = $m.timestamp
    $row.fusion_price = $m.nextFusionPrice; $row.work_price = $m.nextWorkPrice
  } catch { }
  $ds = docker stats --no-stream --format '{{.Name}},{{.CPUPerc}},{{.MemUsage}}' znnd-devnet-rpc 2>$null
  if ($ds) { $f = $ds -split ','; $row.rpc_cpu = $f[1]; $row.rpc_mem = ($f[2] -split ' / ')[0] }
  $line = [pscustomobject]$row
  if ($first) { $line | ConvertTo-Csv -NoTypeInformation | Set-Content $Out; $first = $false }
  else { $line | ConvertTo-Csv -NoTypeInformation | Select-Object -Skip 1 | Add-Content $Out }
  $sleep = $Every - ((Get-Date) - $t0).TotalSeconds
  if ($sleep -gt 0) { Start-Sleep -Milliseconds ([int]($sleep * 1000)) }
}
