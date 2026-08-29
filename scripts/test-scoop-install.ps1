[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[0-9]+\.[0-9]+\.[0-9]+$')]
    [string] $Version,

    [string] $ReleaseDirectory = "dist"
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

if (-not $IsWindows) {
    throw "Scoop installation must be tested on Windows"
}
if ($env:RUNNER_ARCH -and $env:RUNNER_ARCH -ne "X64") {
    throw "Scoop installation requires a native Windows AMD64 runner"
}

$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$releaseRoot = (Resolve-Path $ReleaseDirectory).Path
$preparedManifestPath = Join-Path $releaseRoot "scoop-pl.json"
$archiveName = "pellets_${Version}_windows_amd64.zip"
$archivePath = Join-Path $releaseRoot $archiveName

if (-not (Test-Path -LiteralPath $preparedManifestPath -PathType Leaf)) {
    throw "prepared Scoop manifest not found: $preparedManifestPath"
}
if (-not (Test-Path -LiteralPath $archivePath -PathType Leaf)) {
    throw "Windows release archive not found: $archivePath"
}

$manifest = Get-Content -LiteralPath $preparedManifestPath -Raw | ConvertFrom-Json
$release = $manifest.architecture.'64bit'
$expectedUrl = "https://github.com/isaksky/pellets/releases/download/v$Version/$archiveName"
if ($manifest.version -ne $Version -or $release.url -ne $expectedUrl) {
    throw "prepared Scoop manifest does not select $archiveName for release $Version"
}
if ($manifest.bin -ne "pl.exe") {
    throw "prepared Scoop manifest does not expose pl.exe"
}
$actualHash = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
if ($release.hash -ne $actualHash) {
    throw "prepared Scoop manifest hash does not match $archiveName"
}

$scoopRoot = Join-Path $env:RUNNER_TEMP "pellets-scoop"
if (Test-Path -LiteralPath $scoopRoot) {
    throw "refusing to replace existing Scoop test root: $scoopRoot"
}
$env:SCOOP = $scoopRoot
Set-ExecutionPolicy -ExecutionPolicy RemoteSigned -Scope CurrentUser -Force
Invoke-RestMethod -Uri "https://get.scoop.sh" | Invoke-Expression

$env:PATH = "$(Join-Path $scoopRoot 'shims');$env:PATH"
$scoopCommand = Join-Path $scoopRoot "shims\scoop.cmd"
if (-not (Test-Path -LiteralPath $scoopCommand -PathType Leaf)) {
    throw "Scoop command was not installed at $scoopCommand"
}

function Invoke-Scoop {
    param([Parameter(ValueFromRemainingArguments = $true)][string[]] $Arguments)

    & $scoopCommand @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "scoop $($Arguments -join ' ') failed with exit code $LASTEXITCODE"
    }
}

Invoke-Scoop bucket add pellets $repositoryRoot
$bucketRoot = Join-Path $scoopRoot "buckets\pellets"
$bucketManifestPath = Join-Path $bucketRoot "bucket\pl.json"
if (-not (Test-Path -LiteralPath $bucketManifestPath -PathType Leaf)) {
    throw "repository was not recognized as a Scoop bucket"
}

# Pull requests use a synthetic 0.0.0 archive. Replace only the cloned bucket's
# manifest with the macOS-validated manifest for that archive. On stable tags,
# this copy is byte-identical to the committed manifest checked before publish.
Copy-Item -LiteralPath $preparedManifestPath -Destination $bucketManifestPath -Force

$global:cachedir = Join-Path $scoopRoot "cache"
. (Join-Path $scoopRoot "apps\scoop\current\lib\core.ps1")
$cachePath = cache_path "pl" $Version $release.url
New-Item -ItemType Directory -Path (Split-Path $cachePath) -Force | Out-Null
Copy-Item -LiteralPath $archivePath -Destination $cachePath

Invoke-Scoop install pellets/pl
$plCommand = (Get-Command pl -CommandType Application -ErrorAction Stop).Source
$versionOutput = & $plCommand --version
if ($LASTEXITCODE -ne 0 -or $versionOutput -ne "pl $Version (JSON schema 1)") {
    throw "unexpected Scoop-installed pl --version result: $versionOutput"
}
if (-not $plCommand.StartsWith((Join-Path $scoopRoot "shims"), [StringComparison]::OrdinalIgnoreCase)) {
    throw "Scoop-installed pl did not use the per-user test root: $plCommand"
}

Invoke-Scoop update pl
Invoke-Scoop uninstall pl
if (Get-Command pl -CommandType Application -ErrorAction SilentlyContinue) {
    throw "Scoop uninstall left pl on PATH"
}
Invoke-Scoop bucket rm pellets

Write-Output "verified per-user Scoop install, update, and uninstall for Pellets $Version"
