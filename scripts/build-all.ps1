# Build release binaries for every supported target into dist\ and write
# dist\SHA256SUMS (LF line endings, sha256sum format).
#
#   powershell -NoProfile -File scripts\build-all.ps1 [-Version v0.1.0]
#
# Same go build line as scripts/build-all.sh (the canonical script).
[CmdletBinding()]
param([string]$Version = "")

$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")

if (-not $Version) {
    $Version = (& git describe --tags --always --dirty 2>$null)
    if (-not $Version) { $Version = "dev" }
}

$name = "hermes-safe-update"
$targets = @("windows/amd64", "windows/arm64", "darwin/arm64", "darwin/amd64", "linux/amd64", "linux/arm64")

if (Test-Path dist) { Remove-Item -Recurse -Force dist }
New-Item -ItemType Directory dist | Out-Null

$env:CGO_ENABLED = "0"
try {
    foreach ($t in $targets) {
        $os, $arch = $t.Split("/")
        $ext = if ($os -eq "windows") { ".exe" } else { "" }
        $out = "dist/$name-$os-$arch$ext"
        Write-Host "build $out"
        $env:GOOS = $os
        $env:GOARCH = $arch
        & go build -trimpath -buildvcs=false -ldflags "-s -w -X main.version=$Version" -o $out "./cmd/$name"
        if ($LASTEXITCODE -ne 0) { throw "build failed for $t" }
    }
} finally {
    Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
}

# Licence notices travel with the binaries (BSD-3 terms in docs/third-party.md).
Copy-Item LICENSE, NOTICE, docs/third-party.md dist

# The two Windows launcher shims ship as release assets too (hashed as checked out).
Copy-Item scripts/hermes-safe-update.cmd, scripts/hermes-update-check.cmd dist

$lines = Get-ChildItem dist -File | Where-Object Name -ne "SHA256SUMS" | Sort-Object Name | ForEach-Object {
    "{0}  {1}" -f (Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLower(), $_.Name
}
[IO.File]::WriteAllText((Join-Path (Resolve-Path dist) "SHA256SUMS"), (($lines -join "`n") + "`n"))
Write-Host "wrote dist/SHA256SUMS (version $Version)"
