# Reproducible Windows agent/recovery builds. Do not use plain `go build`.
# Two consecutive runs of this script on the same commit and Go version must match.
param(
    [Parameter(Mandatory = $true)][string]$OutDir,
    [string]$Version = "0.1.1"
)

$ErrorActionPreference = "Stop"
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null

$env:CGO_ENABLED = "0"
$common = @("-trimpath", "-buildvcs=false", "-ldflags", "-buildid=")
# The three desktop programs are GUI-subsystem exes (no console window).
$gui = @("-trimpath", "-buildvcs=false", "-ldflags", "-H windowsgui -buildid=")
$desktop = [ordered]@{
    "Stowline Backups.exe" = "./agent/cmd/stowline-backups"
    "Stowline Admin.exe"    = "./agent/cmd/stowline-admin"
    "Stowline Setup.exe"    = "./agent/cmd/stowline-setup"
}

Push-Location $PSScriptRoot\..
try {
    go build @common -o (Join-Path $OutDir "stowline-agent.exe") .\agent\cmd\stowline-agent
    go build @common -o (Join-Path $OutDir "stowline-recovery.exe") .\recovery\cmd\stowline-recovery
    # Ships in the installer with its own pinned SHA (bootstrap.py's
    # WORKSPACE_REPO_INIT) and imports the same internal packages as the
    # agent, so a change to any of those changes this binary's bytes too
    # even though its own main.go did not change -- it must be rebuilt and
    # reproducibility-checked alongside the agent, not separately by hand.
    go build @common -o (Join-Path $OutDir "workspace-repo-init.exe") .\agent\cmd\workspace-repo-init
    foreach ($name in $desktop.Keys) {
        go build @gui -o (Join-Path $OutDir $name) $desktop[$name]
        if ($LASTEXITCODE -ne 0) { throw "build failed: $name" }
    }
} finally {
    Pop-Location
}

$built = @("stowline-agent.exe", "stowline-recovery.exe", "workspace-repo-init.exe") + @($desktop.Keys) | ForEach-Object { Join-Path $OutDir $_ }
Get-FileHash -LiteralPath $built -Algorithm SHA256 |
    ForEach-Object { "{0}  {1}" -f $_.Hash.ToLower(), (Split-Path $_.Path -Leaf) } |
    Set-Content -Encoding ascii (Join-Path $OutDir "SHA256SUMS")

Write-Host "Built $Version into $OutDir"
Get-Content (Join-Path $OutDir "SHA256SUMS")
