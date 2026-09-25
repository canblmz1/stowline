# Removes the Stowline agent installed on THIS machine: stops and deletes the
# StowlineBackup service, then moves C:\Stowline aside instead of deleting
# it. The folder holds the only local copy of this device's restic
# repository password (DPAPI envelope in secrets\) -- deleting it could
# make the device's existing cloud backups permanently unreadable. The
# archived folder can be removed later, once that key is confirmed in the
# admin panel's Sifre Kasasi (or the old backups are no longer needed).
#
# Must run elevated. Writes a step-by-step log to -LogPath.
param(
    [Parameter(Mandatory = $true)][string]$LogPath
)

$ErrorActionPreference = "Stop"
function Log($msg) { "$(Get-Date -Format s)  $msg" | Out-File -FilePath $LogPath -Append -Encoding utf8 }

try {
    Log "start; elevated=$(([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator))"

    $svc = Get-Service -Name StowlineBackup -ErrorAction SilentlyContinue
    if ($svc) {
        if ($svc.Status -ne "Stopped") {
            Log "stopping StowlineBackup (was $($svc.Status))"
            Stop-Service -Name StowlineBackup -Force
            $svc.WaitForStatus("Stopped", [TimeSpan]::FromSeconds(90))
        }
        Log "deleting StowlineBackup service"
        & sc.exe delete StowlineBackup | Out-Null
        Start-Sleep -Seconds 2
        if (Get-Service -Name StowlineBackup -ErrorAction SilentlyContinue) {
            throw "service still registered after delete"
        }
        Log "service removed"
    } else {
        Log "no StowlineBackup service present"
    }

    # A stopped service can leave its process briefly holding files.
    Get-Process stowline-agent, restic, rclone -ErrorAction SilentlyContinue | ForEach-Object {
        Log "stopping leftover process $($_.Name) pid=$($_.Id)"
        Stop-Process -Id $_.Id -Force -Confirm:$false
    }

    if (Test-Path "C:\Stowline") {
        $archive = "C:\Stowline.archive-$(Get-Date -Format yyyyMMdd-HHmmss)"
        Log "moving C:\Stowline to $archive"
        Rename-Item -Path "C:\Stowline" -NewName (Split-Path $archive -Leaf)
        Log "archived; secrets preserved at $archive\secrets"
    } else {
        Log "no C:\Stowline present"
    }
    Log "DONE"
} catch {
    Log "FAILED: $($_.Exception.Message)"
    exit 1
}
