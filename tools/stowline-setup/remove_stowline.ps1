# Fully removes the Stowline pilot install (Windows service + the
# C:\Stowline tree) so Setup.cmd can run clean again afterward. Every
# step tolerates "already gone" -- safe to re-run, and safe to run when
# nothing is installed at all.
#
# Deliberately does NOT try to preserve the existing device_id: the next
# Setup.cmd run enrolls as a new device, and that is accepted here, not a
# bug -- see wizard.py's duplicate-enrollment guard for the path that
# instead reuses an existing enrollment when that matters.

$ErrorActionPreference = "Continue"
$pilotRoot = "C:\Stowline"
$serviceName = "StowlineBackup"
$failed = $false

function Write-Step {
    param([string]$Message)
    Write-Host "==> $Message"
}

Write-Step "Checking the service..."
$svc = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
if ($svc) {
    if ($svc.Status -ne "Stopped") {
        Write-Step "Stopping the service..."
        try { Stop-Service -Name $serviceName -Force -ErrorAction Stop } catch {}
        for ($i = 0; $i -lt 15; $i++) {
            Start-Sleep -Seconds 1
            $svc.Refresh()
            if ($svc.Status -eq "Stopped") { break }
        }
    }
    Write-Step "Deleting the service registration..."
    $agentExe = Join-Path $pilotRoot "bin\stowline-agent.exe"
    if (Test-Path $agentExe) {
        & $agentExe service remove 2>&1 | Out-Null
        Start-Sleep -Seconds 1
    }
    $still = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
    if ($still) {
        sc.exe delete $serviceName | Out-Null
        Start-Sleep -Seconds 1
    }
    $still2 = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
    if ($still2) {
        Write-Host "ERROR: could not delete the '$serviceName' service registration."
        $failed = $true
    } else {
        Write-Step "Service registration deleted."
    }
} else {
    Write-Step "No service registration, skipping."
}

Write-Step "Closing remaining processes..."
foreach ($name in @("stowline-agent", "restic", "rclone", "workspace-repo-init", "caddy")) {
    Get-Process -Name $name -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
    # taskkill /IM matches by image name directly, without resolving
    # Process.Path -- confirmed live: that property came back empty for a
    # process running above the caller's own privilege level, silently
    # skipping it. A second, path-independent method for exactly the one
    # case already seen to slip past Stop-Process.
    taskkill /F /IM "$name.exe" /T 2>$null | Out-Null
}
# C:\Stowline has accumulated more than the wizard's own layout() --
# repeated ad-hoc testing over one long session left scratch subfolders
# behind (a local Caddy HTTPS test server, qualification-run dumps, etc.).
# Enumerating every name that could ever land in there is whack-a-mole;
# instead kill anything CURRENTLY RUNNING whose own exe path is under this
# tree, whatever its name -- confirmed live: a leftover caddy.exe from an
# earlier HTTPS experiment blocked the delete below with "access denied",
# not "in use", because Windows locks a running executable's image file
# outright. This script must itself run elevated (Remove.cmd's UAC
# self-elevation) for Stop-Process to reach a process elevated like that.
try {
    Get-Process -ErrorAction SilentlyContinue | Where-Object {
        try { $_.Path -and $_.Path.StartsWith($pilotRoot, [System.StringComparison]::OrdinalIgnoreCase) } catch { $false }
    } | ForEach-Object {
        Write-Host "    closing: $($_.ProcessName) (PID $($_.Id))"
        Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue
    }
} catch {}
# Only python.exe processes actually running this installer's own wizard --
# never touch an unrelated Python process the operator happens to have open.
try {
    Get-CimInstance Win32_Process -Filter "Name='python.exe'" -ErrorAction SilentlyContinue |
        Where-Object { $_.CommandLine -and ($_.CommandLine -match "stowline-setup" -or $_.CommandLine -match "wizard\.py" -or $_.CommandLine -match "bootstrap\.py") } |
        ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
} catch {}
Start-Sleep -Seconds 1

if (Test-Path $pilotRoot) {
    Write-Step "Deleting $pilotRoot..."
    $deleted = $false
    $lastError = ""
    for ($i = 1; $i -le 6; $i++) {
        try {
            Remove-Item -Path $pilotRoot -Recurse -Force -ErrorAction Stop
            $deleted = $true
            break
        } catch {
            $lastError = $_.Exception.Message
            Start-Sleep -Seconds 2
        }
    }
    if ($deleted) {
        Write-Step "$pilotRoot deleted."
    } else {
        Write-Host "ERROR: could not delete $pilotRoot."
        Write-Host "Cause: $lastError"
        Write-Host "If a file or process is shown above, close it and run Remove.cmd again."
        $failed = $true
    }
} else {
    Write-Step "$pilotRoot is already gone."
}

Write-Host ""
if ($failed) {
    Write-Host "CLEANUP DID NOT COMPLETE -- see the ERROR lines above."
    exit 1
} else {
    Write-Host "CLEAN. You can run Setup.cmd now -- it enrolls as a new device, as expected."
    exit 0
}
