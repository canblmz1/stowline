# Downloads the restic and rclone builds the installer ships, checks them
# against the upstream SHA256SUMS, and puts restic.exe and rclone.exe into
# releases\_cache (where scripts/package-installer.py looks by default).
# package-installer.py then checks them again against release/manifest.json.
param([string]$OutDir = (Join-Path $PSScriptRoot '..\releases\_cache'))
$ErrorActionPreference = 'Stop'
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
$OutDir = (Resolve-Path $OutDir).Path
$manifest = Get-Content -Raw (Join-Path $PSScriptRoot '..\release\manifest.json') | ConvertFrom-Json

function Assert-Hash([string]$path, [string]$expected) {
  if ($expected -notmatch '^[a-fA-F0-9]{64}$') { throw "Invalid SHA-256 pin for $path" }
  $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $path).Hash.ToLower()
  if ($actual -ne $expected.ToLower()) { throw "HASH_MISMATCH $path $actual $expected" }
}

# Cached files are trusted only after checking the committed executable pins.
if ((Test-Path "$OutDir\restic.exe") -and (Test-Path "$OutDir\rclone.exe")) {
  Assert-Hash "$OutDir\restic.exe" $manifest.restic_sha256
  Assert-Hash "$OutDir\rclone.exe" $manifest.rclone_sha256
  Write-Host 'Verified cached restic and rclone executable pins.'
  exit 0
}
$tmp = Join-Path $OutDir ('tmp-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path $tmp | Out-Null

function Fetch([string]$uri, [string]$destination) {
  & curl.exe --fail --location --retry 3 --connect-timeout 20 --max-time 180 --silent --show-error $uri --output $destination
  if ($LASTEXITCODE -ne 0) { throw "Download failed: $uri" }
}
try {
  $resticArchive = "restic_$($manifest.restic_version)_windows_amd64.zip"
  $rcloneArchive = "rclone-v$($manifest.rclone_version)-windows-amd64.zip"
  Fetch "https://github.com/restic/restic/releases/download/v$($manifest.restic_version)/SHA256SUMS" "$tmp\restic-SHA256SUMS"
  Fetch "https://github.com/restic/restic/releases/download/v$($manifest.restic_version)/$resticArchive" "$tmp\restic.zip"
  Fetch "https://downloads.rclone.org/v$($manifest.rclone_version)/SHA256SUMS" "$tmp\rclone-SHA256SUMS"
  Fetch "https://downloads.rclone.org/v$($manifest.rclone_version)/$rcloneArchive" "$tmp\rclone.zip"
  $resticLine = @(Get-Content "$tmp\restic-SHA256SUMS" | Where-Object { ($_ -split '\s+')[-1] -eq $resticArchive })
  $rcloneLine = @(Get-Content "$tmp\rclone-SHA256SUMS" | Where-Object { ($_ -split '\s+')[-1] -eq $rcloneArchive })
  if ($resticLine.Count -ne 1 -or $rcloneLine.Count -ne 1) { throw 'Missing or ambiguous upstream checksum' }
  Assert-Hash "$tmp\restic.zip" (($resticLine[0] -split '\s+')[0])
  Assert-Hash "$tmp\rclone.zip" (($rcloneLine[0] -split '\s+')[0])
  Expand-Archive -Path "$tmp\restic.zip" -DestinationPath "$tmp\restic"
  Expand-Archive -Path "$tmp\rclone.zip" -DestinationPath "$tmp\rclone"
  $resticExe = @(Get-ChildItem -Recurse "$tmp\restic" -Filter '*.exe')
  $rcloneExe = @(Get-ChildItem -Recurse "$tmp\rclone" -Filter 'rclone.exe')
  if ($resticExe.Count -ne 1 -or $rcloneExe.Count -ne 1) { throw 'Missing or ambiguous executable in archive' }
  Assert-Hash $resticExe[0].FullName $manifest.restic_sha256
  Assert-Hash $rcloneExe[0].FullName $manifest.rclone_sha256
  Copy-Item -Force -LiteralPath $resticExe[0].FullName -Destination "$OutDir\restic.exe"
  Copy-Item -Force -LiteralPath $rcloneExe[0].FullName -Destination "$OutDir\rclone.exe"
} finally {
  $resolvedTemp = (Resolve-Path -LiteralPath $tmp).Path
  if ((Split-Path -Parent $resolvedTemp) -ne $OutDir) { throw 'Unsafe download cleanup path' }
  Remove-Item -LiteralPath $resolvedTemp -Recurse -Force
}
Write-Host "restic.exe $((Get-FileHash "$OutDir\restic.exe").Hash)"
Write-Host "rclone.exe $((Get-FileHash "$OutDir\rclone.exe").Hash)"
