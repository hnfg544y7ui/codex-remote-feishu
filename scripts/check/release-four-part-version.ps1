$ErrorActionPreference = "Stop"
$rootDir = Split-Path (Split-Path $PSScriptRoot -Parent) -Parent
$installerPath = Join-Path $rootDir "install-release.ps1"
$tokens = $null
$parseErrors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile($installerPath, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -gt 0) { throw "Installer PowerShell syntax errors: $parseErrors" }
$lookup = $ast.Find({ param($node) $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq "Resolve-LatestVersionFromReleaseApi" }, $true)
Invoke-Expression $lookup.Extent.Text
$ReleasesApiUrl = "fixture"
function Invoke-TextRequest([string]$Url) { return $script:releaseFixture }
function Assert-Latest([string]$Track, [string]$Expected) {
  $actual = Resolve-LatestVersionFromReleaseApi $Track
  if ($actual -ne $Expected) { throw "${Track}: expected $Expected, got $actual" }
}
$script:releaseFixture = '[{"tag_name":"v1.0.0.1","draft":false,"prerelease":false},{"tag_name":"v2.1.2","draft":false,"prerelease":false}]'
Assert-Latest production v1.0.0.1
$script:releaseFixture = '[{"tag_name":"v1.0.0.1-beta.2","draft":false,"prerelease":true},{"tag_name":"v1.0.0.1","draft":false,"prerelease":false}]'
Assert-Latest beta v1.0.0.1-beta.2
Assert-Latest production v1.0.0.1
$script:releaseFixture = '[{"tag_name":"v1.0.0.1.2","draft":false,"prerelease":false},{"tag_name":"v2.1.2","draft":false,"prerelease":false}]'
Assert-Latest production v2.1.2
$script:releaseFixture = '[{"tag_name":"v1.0.0.1-alpha.3","draft":false,"prerelease":true}]'
Assert-Latest alpha v1.0.0.1-alpha.3
Write-Output "four-part Windows installer lookup selftest: ok"
