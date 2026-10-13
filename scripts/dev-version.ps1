# Same result as scripts/dev-version.sh (read its header for the format and
# the rules), for build-all.ps1. Dot-source this file, then call
# Get-DevVersion; it prints nothing by itself.
function Get-DevVersion {
    $root = Join-Path $PSScriptRoot ".."
    Push-Location $root
    try {
        $base = $null
        if (Test-Path CHANGELOG.md) {
            foreach ($line in Get-Content CHANGELOG.md) {
                if ($line -match '^## \[(\d+\.\d+\.\d+)\]') { $base = $Matches[1]; break }
            }
        }
        & git rev-parse --verify -q HEAD 2>$null | Out-Null
        if (-not $base -or $LASTEXITCODE -ne 0) { return "dev" }

        $anchor = & git rev-parse --verify -q "refs/tags/v$base^{commit}" 2>$null
        if ($LASTEXITCODE -ne 0 -or -not $anchor) {
            $anchor = $null
            $found = @(& git log "-S## [$base]" --format=%H -- CHANGELOG.md 2>$null)
            if ($found.Count -gt 0) { $anchor = $found[-1] }
        }

        $sha = (& git rev-parse --short=7 HEAD).Trim()
        & git diff-index --quiet HEAD -- 2>$null
        $dirty = if ($LASTEXITCODE -ne 0) { ".dirty" } else { "" }

        if (-not $anchor) { return "v$base+$sha$dirty" }
        $n = [int](& git rev-list --count "$anchor..HEAD")
        if ($n -eq 0 -and -not $dirty) { return "v$base" }
        return "v$base+$n.$sha$dirty"
    } finally {
        Pop-Location
    }
}
