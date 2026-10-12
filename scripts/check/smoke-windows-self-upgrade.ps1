param(
  [Parameter(Mandatory = $true)][string]$OldBinary,
  [Parameter(Mandatory = $true)][string]$TargetBinary,
  [Parameter(Mandatory = $true)][string]$WorkspaceRoot
)
# Build both inputs from testkit/windowsupgradefixture, with different main.version
# values. All install/helper/service/stop logic is production; HTTP health is a
# deterministic fixture. This does not validate a live Feishu connection.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$utf8 = New-Object Text.UTF8Encoding($false)
$root = (Resolve-Path -LiteralPath $WorkspaceRoot).Path.TrimEnd('\')
$old = (Resolve-Path -LiteralPath $OldBinary).Path
$target = (Resolve-Path -LiteralPath $TargetBinary).Path
$run = Join-Path $root ('windows-self-upgrade-' + [Guid]::NewGuid().ToString('N'))
$proof = Join-Path $root ([IO.Path]::GetFileName($run) + '-proof.json')
$log = Join-Path $root ([IO.Path]::GetFileName($run) + '.log')
$scenarios = @()
$ownedTasks = @()
$ownedHelpers = @{}
$completed = $false

function Assert-InRun([string]$Path) {
  $absolute = [IO.Path]::GetFullPath($Path)
  if (!$absolute.StartsWith($run + '\', [StringComparison]::OrdinalIgnoreCase)) {
    throw "Refusing operation outside unique test directory: $absolute"
  }
}
function Get-TestProcesses {
  return @(Get-CimInstance Win32_Process | Where-Object {
    $_.ExecutablePath -and $_.ExecutablePath.StartsWith($run + '\', [StringComparison]::OrdinalIgnoreCase)
  })
}
function Get-FreePort {
  $listener = New-Object Net.Sockets.TcpListener([Net.IPAddress]::Loopback, 0)
  $listener.Start()
  try { return $listener.LocalEndpoint.Port } finally { $listener.Stop() }
}
function Write-Json([string]$Path, $Value) {
  Assert-InRun $Path
  [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($Path)) | Out-Null
  [IO.File]::WriteAllText($Path, ($Value | ConvertTo-Json -Depth 20), $utf8)
}
function Read-Json([string]$Path) {
  return (Get-Content -LiteralPath $Path -Raw -Encoding UTF8 | ConvertFrom-Json)
}
function Invoke-Cli([string]$Binary, [string[]]$Arguments) {
  $output = & $Binary @Arguments 2>&1
  $exit = $LASTEXITCODE
  $output | Out-File -LiteralPath $log -Append -Encoding UTF8
  if ($exit -ne 0) { throw "Fixture CLI failed: $($Arguments[0]), exit $exit" }
  return ($output -join "`n").Trim()
}
function Invoke-LocalGet([string]$Url) {
  $client = New-Object Net.WebClient
  $client.Proxy = $null
  try { return $client.DownloadString($Url) } finally { $client.Dispose() }
}
function Wait-Health([string]$Origin, [string]$Version) {
  $deadline = [DateTime]::UtcNow.AddSeconds(45)
  while ([DateTime]::UtcNow -lt $deadline) {
    try {
      $identity = Invoke-LocalGet ($Origin + '/v1/status') | ConvertFrom-Json
      if ($identity.version -eq $Version -and (Invoke-LocalGet ($Origin + '/healthz')) -eq 'ok') { return $identity }
    } catch {}
    Start-Sleep -Milliseconds 200
  }
  throw "Timed out waiting for fixture daemon $Version"
}
function Collect-HelperOwnership {
  foreach ($directory in @(Get-ChildItem -LiteralPath ([IO.Path]::GetTempPath()) -Directory -Filter 'CodexRemoteUpgradeHelper-*')) {
    $specPath = Join-Path $directory.FullName 'spec.json'
    try {
      $spec = Read-Json $specPath
      if ($spec.BinaryPath.StartsWith($run + '\', [StringComparison]::OrdinalIgnoreCase)) {
        $ownedHelpers[$directory.Name] = $directory.FullName
      }
    } catch {}
  }
}
function Wait-HelperCleanup {
  $deadline = [DateTime]::UtcNow.AddSeconds(20)
  do {
    Collect-HelperOwnership
    $remaining = @($ownedHelpers.Keys | Where-Object {
      (Test-Path -LiteralPath $ownedHelpers[$_]) -or (Get-ScheduledTask -TaskName $_ -ErrorAction SilentlyContinue)
    })
    if ($remaining.Count -eq 0) { return }
    Start-Sleep -Milliseconds 200
  } while ([DateTime]::UtcNow -lt $deadline)
  throw 'Owned upgrade helper tasks/artifacts did not clean themselves up'
}
function Run-Scenario([string]$Name, [bool]$FailTarget) {
  $base = Join-Path $run $Name
  $bin = Join-Path $base 'bin'
  $instance = 'upgrade-smoke-' + [Guid]::NewGuid().ToString('N').Substring(0, 16)
  $namespace = 'codex-remote-' + $instance
  $stateDir = Join-Path $base ('.local\share\' + $namespace + '\codex-remote')
  $runtimeDir = Join-Path $base ('.local\state\' + $namespace + '\codex-remote')
  $statePath = Join-Path $stateDir 'install-state.json'
  $configPath = Join-Path $base ('.config\' + $namespace + '\codex-remote\config.json')
  $live = Join-Path $bin 'codex-remote.exe'
  $taskName = $instance
  if (Get-ScheduledTask -TaskPath '\CodexRemoteFeishu\' -TaskName $taskName -ErrorAction SilentlyContinue) {
    throw 'Unique test task already exists; refusing to reuse it'
  }
  $script:ownedTasks += $taskName
  $relayPort = Get-FreePort
  $adminPort = Get-FreePort
  $toolPort = Get-FreePort
  Write-Json $configPath @{
    version = 2
    relay = @{listenHost = '127.0.0.1'; listenPort = $relayPort; serverURL = "ws://127.0.0.1:$relayPort/ws/agent"}
    admin = @{listenHost = '127.0.0.1'; listenPort = $adminPort; autoOpenBrowser = $false}
    tool = @{listenHost = '127.0.0.1'; listenPort = $toolPort}
    feishu = @{apps = @()}
    wrapper = @{codexRealBinary = 'unavailable-test-only'; codexRuntime = @{source = 'external'; autoUpdate = $false}}
    debug = @{pprof = @{enabled = $false}}
  }
  $null = Invoke-Cli $old @('install', '-bootstrap-only', '-instance', $instance, '-base-dir', $base,
    '-install-bin-dir', $bin, '-binary', $old, '-service-manager', 'task_scheduler_logon',
    '-install-source', 'release', '-current-version', $oldVersion, '-current-track', 'production')
  $live = [string](Read-Json $statePath).currentBinaryPath
  Assert-InRun $live
  # The bootstrap normalizes configuration once; preserve that exact byte stream.
  $configHash = (Get-FileHash -LiteralPath $configPath -Algorithm SHA256).Hash
  $null = Invoke-Cli $live @('service', 'install-user', '-state-path', $statePath)
  $null = Invoke-Cli $live @('service', 'start', '-state-path', $statePath)
  $origin = "http://127.0.0.1:$adminPort"
  $oldIdentity = Wait-Health $origin $oldVersion
  $oldDaemon = [int]$oldIdentity.pid
  $oldWorker = [int](Get-Content -LiteralPath (Join-Path $runtimeDir 'fixture-worker.pid') -Raw)
  if (!(Get-Process -Id $oldWorker -ErrorAction SilentlyContinue)) { throw 'Old executable-holding worker missing' }
  $artifact = Join-Path $stateDir 'local-upgrade\codex-remote.exe'
  Assert-InRun $artifact
  New-Item -ItemType Directory -Path ([IO.Path]::GetDirectoryName($artifact)) -Force | Out-Null
  Copy-Item -LiteralPath $target -Destination $artifact
  if ($FailTarget) { [IO.File]::WriteAllText($configPath + '.fail-version', $targetVersion, $utf8) }
  $request = [Net.HttpWebRequest]::Create($origin + '/fixture/upgrade')
  $request.Proxy = $null; $request.Method = 'POST'; $request.ContentLength = 0; $request.Timeout = 45000
  $response = $request.GetResponse()
  $response.Close()
  # The upgrade originates inside the daemon being stopped by Task Scheduler.
  $deadline = [DateTime]::UtcNow.AddSeconds(105)
  $expectedPhase = 'committed'; $expectedVersion = $targetVersion
  if ($FailTarget) { $expectedPhase = 'rolled_back'; $expectedVersion = $oldVersion }
  do {
    Collect-HelperOwnership
    $state = Read-Json $statePath
    if ($state.pendingUpgrade.phase -eq $expectedPhase -and $state.currentVersion -eq $expectedVersion) { break }
    if ($state.pendingUpgrade.phase -eq 'failed') { throw "Upgrade helper failed for $Name" }
    Start-Sleep -Milliseconds 200
  } while ([DateTime]::UtcNow -lt $deadline)
  if ($state.pendingUpgrade.phase -ne $expectedPhase) { throw "Journal stuck at $($state.pendingUpgrade.phase)" }
  if ($state.currentVersion -ne $expectedVersion) { throw "Journal version mismatch: $($state.currentVersion)" }
  $newIdentity = Wait-Health $origin $expectedVersion
  if ([int]$newIdentity.pid -eq $oldDaemon) { throw 'Daemon PID did not change' }
  foreach ($processId in @($oldDaemon, $oldWorker)) {
    if (Get-Process -Id $processId -ErrorAction SilentlyContinue) { throw "Old fixture PID still running: $processId" }
  }
  $installedVersion = Invoke-Cli $live @('version')
  if ($installedVersion -ne $expectedVersion) { throw 'Installed binary version incorrect' }
  if ((Get-FileHash -LiteralPath $configPath -Algorithm SHA256).Hash -ne $configHash) { throw 'Config bytes changed during upgrade' }
  Wait-HelperCleanup
  Get-ChildItem -LiteralPath $stateDir -Recurse -File -Filter '*.log' | ForEach-Object {
    "--- $Name $($_.Name) ---" | Out-File -LiteralPath $log -Append -Encoding UTF8
    Get-Content -LiteralPath $_.FullName -Encoding UTF8 -Tail 45 | Out-File -LiteralPath $log -Append -Encoding UTF8
  }
  $script:scenarios += @{name = $Name; phase = $state.pendingUpgrade.phase; version = $installedVersion;
    journalVersion = $state.currentVersion; healthyVersion = $newIdentity.version;
    oldDaemon = $oldDaemon; oldWorker = $oldWorker; newDaemon = [int]$newIdentity.pid;
    unchangedConfigHash = $configHash; helperCleanup = $true}
  $null = Invoke-Cli $live @('service', 'stop', '-state-path', $statePath)
  $null = Invoke-Cli $live @('service', 'uninstall-user', '-state-path', $statePath)
}

try {
  New-Item -ItemType Directory -Path $run | Out-Null
  $oldVersion = Invoke-Cli $old @('version')
  $targetVersion = Invoke-Cli $target @('version')
  if ($oldVersion -eq $targetVersion) { throw 'Two distinct injected fixture versions are required' }
  Run-Scenario 'success' $false
  Run-Scenario 'rollback' $true
  $completed = $true
} finally {
  Collect-HelperOwnership
  if (Test-Path -LiteralPath $run) {
    Get-ChildItem -LiteralPath $run -Recurse -File -Filter '*.log' | ForEach-Object {
      "--- cleanup evidence $($_.Name) ---" | Out-File -LiteralPath $log -Append -Encoding UTF8
      Get-Content -LiteralPath $_.FullName -Encoding UTF8 -Tail 45 | Out-File -LiteralPath $log -Append -Encoding UTF8
    }
  }
  foreach ($taskName in $ownedTasks) {
    Stop-ScheduledTask -TaskPath '\CodexRemoteFeishu\' -TaskName $taskName -ErrorAction SilentlyContinue
    Unregister-ScheduledTask -TaskPath '\CodexRemoteFeishu\' -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue
  }
  foreach ($taskName in @($ownedHelpers.Keys)) {
    Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
    Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue
  }
  foreach ($process in (Get-TestProcesses)) {
    Stop-Process -Id $process.ProcessId -Force -ErrorAction SilentlyContinue
  }
  Start-Sleep -Milliseconds 300
  $remainingProcesses = Get-TestProcesses
  $remainingTasks = @($ownedTasks | Where-Object { Get-ScheduledTask -TaskPath '\CodexRemoteFeishu\' -TaskName $_ -ErrorAction SilentlyContinue })
  if ($remainingProcesses.Count -gt 0 -or $remainingTasks.Count -gt 0) { throw 'Isolated smoke cleanup incomplete' }
  # Only recorded helper directories whose spec proved exact ownership may be removed.
  foreach ($directory in @($ownedHelpers.Values)) {
    $helperRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\'
    if ((Test-Path -LiteralPath $directory) -and
      [IO.Path]::GetFullPath($directory).StartsWith($helperRoot, [StringComparison]::OrdinalIgnoreCase) -and
      [IO.Path]::GetFileName($directory) -match '^CodexRemoteUpgradeHelper-[a-f0-9]{32}$') {
      Remove-Item -LiteralPath $directory -Recurse -Force
    }
  }
  if ([IO.Path]::GetFullPath($run).StartsWith($root + '\', [StringComparison]::OrdinalIgnoreCase) -and (Test-Path -LiteralPath $run)) {
    Remove-Item -LiteralPath $run -Recurse -Force
  }
  [IO.File]::WriteAllText($proof, (@{passed = $completed; scenarios = $scenarios; cleanup = $true; log = $log;
    oldBinarySha256 = (Get-FileHash -LiteralPath $old -Algorithm SHA256).Hash;
    targetBinarySha256 = (Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash} | ConvertTo-Json -Depth 10), $utf8)
}
Write-Output "Windows self-upgrade success and rollback smoke passed. Proof: $proof"
