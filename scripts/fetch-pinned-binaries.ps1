# Downloads the restic and rclone builds the installer ships, checks them
# against the upstream SHA256SUMS, and puts restic.exe and rclone.exe into
# releases\_cache (where scripts/package-installer.py looks by default).
# package-installer.py then checks them again against release/manifest.json.
param([string]$OutDir = (Join-Path $PSScriptRoot '..\releases\_cache'))
$ErrorActionPreference = 'Stop'
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
$OutDir = (Resolve-Path $OutDir).Path
$tmp = Join-Path $OutDir 'tmp-downloads'
New-Item -ItemType Directory -Force -Path $tmp | Out-Null

Invoke-WebRequest -UseBasicParsing -Uri 'https://github.com/restic/restic/releases/download/v0.19.1/SHA256SUMS' -OutFile "$tmp\restic-SHA256SUMS"
Invoke-WebRequest -UseBasicParsing -Uri 'https://github.com/restic/restic/releases/download/v0.19.1/restic_0.19.1_windows_amd64.zip' -OutFile "$tmp\restic.zip"
Invoke-WebRequest -UseBasicParsing -Uri 'https://downloads.rclone.org/v1.75.0/SHA256SUMS' -OutFile "$tmp\rclone-SHA256SUMS"
Invoke-WebRequest -UseBasicParsing -Uri 'https://downloads.rclone.org/v1.75.0/rclone-v1.75.0-windows-amd64.zip' -OutFile "$tmp\rclone.zip"

function Assert-Hash([string]$path, [string]$expected) {
  $actual = (Get-FileHash -Algorithm SHA256 -Path $path).Hash.ToLower()
  if ($actual -ne $expected.ToLower()) { throw "HASH_MISMATCH $path $actual $expected" }
}
$resticHash = ((Select-String -Path "$tmp\restic-SHA256SUMS" -Pattern 'restic_0.19.1_windows_amd64.zip').Line -split '\s+')[0]
$rcloneHash = ((Select-String -Path "$tmp\rclone-SHA256SUMS" -Pattern 'rclone-v1.75.0-windows-amd64.zip').Line -split '\s+')[0]
Assert-Hash "$tmp\restic.zip" $resticHash
Assert-Hash "$tmp\rclone.zip" $rcloneHash
Expand-Archive -Force -Path "$tmp\restic.zip" -DestinationPath "$tmp\restic"
Expand-Archive -Force -Path "$tmp\rclone.zip" -DestinationPath "$tmp\rclone"
Copy-Item -Force (Get-ChildItem -Recurse "$tmp\restic" -Filter '*.exe' | Select-Object -First 1).FullName "$OutDir\restic.exe"
Copy-Item -Force (Get-ChildItem -Recurse "$tmp\rclone" -Filter 'rclone.exe' | Select-Object -First 1).FullName "$OutDir\rclone.exe"
Remove-Item -Recurse -Force $tmp
Write-Host "restic.exe $((Get-FileHash "$OutDir\restic.exe").Hash)"
Write-Host "rclone.exe $((Get-FileHash "$OutDir\rclone.exe").Hash)"
