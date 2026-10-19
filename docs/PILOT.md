# Windows pilot and independent recovery

Use this with an independent IT administrator and 2–3 disposable Windows
computers, across two sites if possible. Use synthetic files and a dedicated
storage root. The goal is a verified file recovery by the operator, followed
by recovery without the original computer and an update that keeps enrollment.
This is file backup, not a disk image or bare-metal recovery procedure.

## Prepare the pilot

1. Follow [Deployment](DEPLOYMENT.md) with your own rclone remote and unused
   root, for example `pilot-store:stowline-pilot`. Set the gateway root to
   `rclone:pilot-store:stowline-pilot`. Confirm that the control plane can reach
   the matching gateway admin API, so the recovery guide can resolve its real
   root and registry. Keep storage credentials on the gateway and the trusted
   recovery workstation.
2. Pin the tested server/gateway version and record the source commit, image
   digests, installer version and binary SHA-256 values. Keep the matching
   `release/manifest.json` with the recovery tools. An unqualified agent build
   must remain unqualified until the real Windows checks below pass.
3. Create site schedules and measured WAN budgets suitable for your test
   connections. Create a setup code restricted to these computers. Record the
   Windows version, site, device ID, repository ID, generation and permanent
   repository source for each enrollment. The recovery guide supplies the
   source; do not reconstruct it from current department/hostname labels.
   If the guide reports missing location information, resolve it before the
   lost-device test.
4. Create a small test folder containing text, binary and empty files. Save an
   inventory of relative paths, lengths and SHA-256 values outside the test
   computer. Keep enough free space for both a repository copy and restored
   files on the recovery workstation.
5. Export each computer's backup key using
   [the existing key export tool](../scripts/key-escrow/README.txt), check the
   saved copy with its verification prompt, and store it in the operator's
   password manager/offline custody. Record only the fingerprint in the pilot
   notes. Optionally upload an additional Key Vault copy. The fingerprint is
   not a recovery key; the endpoint's DPAPI files alone are not a portable key.

## Complete the chain

1. **Enroll and back up.** Install using the setup code, choose only the
   synthetic folder, and complete the first backup. Record the snapshot ID and
   outcome. Test the site bandwidth cap and an offline/reconnect cycle without
   claiming performance beyond the measured test connection.
2. **Verify a file restore.** Restore the selected snapshot through the normal
   device/panel flow into staging. Compare the restored file bytes or SHA-256
   values with the independently saved inventory; open the text file. Record
   missing/extra files as failures. A green backup result alone does not pass.
3. **Recover without the original computer.** Shut down or disconnect that
   test computer. On a separate trusted workstation with no enrollment,
   endpoint secrets or agent configuration, follow the procedure below using
   the retained key, recorded repository source and storage access. Verify all
   test files again. Do not enroll the recovery workstation to perform this.
4. **Update while preserving enrollment.** Reconnect the test computer. Follow
   [Agent updates](DEPLOYMENT.md#6-agent-updates), initially selecting one
   canary device ID. Before and after, compare device ID, repository/generation,
   key fingerprint and existing snapshot history. Verify a new backup and a
   restore of an old snapshot; the device count must not increase. Verify that
   a busy backup and a busy restore defer restart, and that a failed candidate
   health check rolls back. Then include the other selected test devices. Keep
   these results separate from automated tests; do not mark `agent_qualified`
   successful until the required real Windows qualification passes.
5. **Change labels.** Rename one device's panel label/department and confirm
   that its recorded storage source and generation still locate the old
   snapshots. No repository move is part of this pilot.

## Recovery from a clean workstation

You need these four things, available independently of the original computer:

| Material | Required for |
|---|---|
| Complete encrypted repository copy, including `config`, `keys`, `data`, `index`, `snapshots` and the selected generation | Opening the backup |
| The backup password for that repository/generation, from verified off-device custody or Key Vault | Decrypting it |
| Exact remote/root plus persistent storage prefix and generation, and storage read access / rclone configuration (plus its config password if encrypted) | Fetching the copy |
| Trusted `stowline-recovery.exe`, restic and rclone binaries, with hashes checked against the matching trusted release/package | Running the recovery |

The gateway device credential is not a storage credential. The panel admin
password is not the backup password. A database dump or Key Vault ciphertext
without `STOWLINE_ESCROW_KEY` cannot replace a missing backup key.

For a consistent copy, pause new pilot backups and wait for running work to
finish before downloading. Use a fresh case directory each time. The commands
below operate on local copies; the original repository remains the source.
Use the exact source recorded from the authorized recovery guide, including
the generation. The example path is a placeholder, not a naming algorithm.

```powershell
$ErrorActionPreference = 'Stop'
$Tools = 'C:\Recovery\tools'
$Case = 'C:\Recovery\case-001' # choose a new directory for every attempt
$RepositorySource = 'pilot-store:stowline-pilot/<recorded-prefix>/<generation>'
$RcloneConfig = 'C:\Recovery\private\rclone.conf'
$ResticPin = (Get-Content "$Tools\manifest.json" -Raw | ConvertFrom-Json).restic_sha256

function Invoke-Required([scriptblock]$Action) {
    & $Action
    if ($LASTEXITCODE -ne 0) { throw "Recovery command failed: $LASTEXITCODE" }
}
if (Test-Path -LiteralPath $Case) { throw 'Choose a fresh case directory' }
New-Item -ItemType Directory -Path $Case | Out-Null
Invoke-Required { & "$Tools\rclone.exe" --config $RcloneConfig copy $RepositorySource "$Case\download" }
Invoke-Required { & "$Tools\stowline-recovery.exe" copy --src "$Case\download" --dst "$Case\repository" }

# Read securely: no literal key in PowerShell history or the pilot notes.
$Key = Read-Host 'Retained backup key for this repository' -AsSecureString
try {
    $env:RESTIC_PASSWORD = [System.Net.NetworkCredential]::new('', $Key).Password
    Invoke-Required { & "$Tools\stowline-recovery.exe" verify --repo "$Case\repository" --restic "$Tools\restic.exe" --sha256 $ResticPin }
    Invoke-Required { & "$Tools\stowline-recovery.exe" list-snapshots --repo "$Case\repository" --restic "$Tools\restic.exe" --sha256 $ResticPin }
    $Snapshot = Read-Host 'Snapshot ID to restore'
    Invoke-Required { & "$Tools\restic.exe" --repo "$Case\repository" ls $Snapshot }
    $SnapshotPath = Read-Host 'Folder inside snapshot (copy its path from the listing)'
    if (-not $SnapshotPath.StartsWith('/')) { throw 'Select an absolute path inside the snapshot' }
    $Selector = "${Snapshot}:$SnapshotPath"
    Invoke-Required { & "$Tools\stowline-recovery.exe" restore-to-staging --repo "$Case\repository" --restic "$Tools\restic.exe" --sha256 $ResticPin --snapshot $Selector --staging "$Case\staging" }
} finally {
    Remove-Item Env:RESTIC_PASSWORD -ErrorAction SilentlyContinue
    $Key.Dispose()
}
```

`copy` converts the gateway's flat restic pack layout to the sharded layout
expected by restic's local backend. Do not run restic directly on the raw
download and assume a successful copy proves a usable local repository.
The `snapshotID:/folder` selector restores only the selected subtree. It avoids
recreating original Windows ancestor directories such as `C:\Users` with their
original ACLs on the recovery workstation. Whole-snapshot restoration of those
ACLs needs separate Windows validation. `verify` runs restic `check`; it does
not compare recovered files with the original inventory. Compare the staged
files yourself, record the snapshot,
relative paths, sizes and hashes, then copy approved files to their intended
locations. The generated recovery manifests record a local tree hash; their
`independent` field is not proof that an external operator completed recovery.

Keep these three statuses separate in the pilot record:

| Status | Evidence |
|---|---|
| Backup completed | Snapshot ID and successful backup outcome |
| Key retained outside endpoint | Saved copy verified, fingerprint and custody reference; no plaintext key |
| Recovery verified | Named snapshot restored, expected file bytes/hashes matched, who verified and when |

## Control-plane disaster material

The independent procedure above needs neither the Stowline database nor a
running control plane once repository source, storage access and backup key
are retained. To resume management of the same fleet, also retain:

- a restorable PostgreSQL dump (devices, permanent repository mappings,
  selections, history, audit records and sealed Key Vault copies);
- the original `STOWLINE_ESCROW_KEY`, stored separately from the dump;
- private deployment environment/configuration, including server secret,
  gateway admin token, storage root, `rclone.conf` and its encryption password,
  sites file, reverse proxy/TLS/DNS configuration and release pins/artifacts;
- the gateway registry (`registry.json`) and any local backend repository data.

Protect and test these materials as secrets. A database restore must be tried
in an isolated control plane with production endpoints unable to connect.
The systemd deployment's nightly `stowline-dbdump` keeps local dumps for 14 days
and copies them off-server only when `STOWLINE_DBDUMP_REMOTE` is configured.
Compose does not install that timer: arrange and verify its database/config
backups separately. A remote dump upload does not establish that the dump can
be restored or that storage credentials/configuration were included.

## Retention and maintenance ownership

The IT operator owns retention, storage capacity monitoring and maintenance.
The agent/gateway do not automatically execute `forget` or `prune`; policy
values in configuration do not schedule deletion. Existing repositories grow
until an operator performs maintenance with direct storage access.

For this pilot, do not delete backup history. Use the recovery tool's
`forget-plan` only on a verified disposable repository copy to inspect the
default 7 daily / 5 weekly / 12 monthly policy. `forget-apply` removes snapshot
references after explicit flags; it does not run `prune` or reclaim all unused
packs, and its confirmation token is an operator acknowledgement, not a
server-issued approval. Design and approve live retention separately, after a
verified independent recovery copy and a maintenance window without writers.

## Evidence and decision

Keep one short private record per operator, with no passwords, tokens, raw
keys or sensitive file paths. Record UTC start time, first verified restore
time, elapsed minutes, step that blocked, exact intervention/help required,
Windows/build versions, device/repository/generation references, snapshot ID,
expected/restored hash result and whether the original device was unavailable.
Record the update identity/history checks and rollback result separately.

Ask the independent administrator whether they would keep using Stowline and
why. Evaluate time to first verified restore, number and kind of interventions,
and that continuation decision. Maintainer demo data and automated tests do
not count as independent adoption. Keep mock tests, real-engine automated
tests and real Windows device qualification as separate evidence categories;
engine tests alone establish no VSS or installed-service behavior.

Maintainer verification on 2026-10-19: the recovery Go package passed all 12
tests on Windows with `STOWLINE_REQUIRE_REAL_ENGINES=1`, restic 0.19.1 and rclone
1.75.0 (binary hashes matched the release manifest). The real-engine test
created a synthetic backup, copied a gateway-style flat pack layout through
rclone's local backend, removed the original source/repository, then checked,
listed and restored the selected subtree from the independent copy. Text,
binary and empty files matched exact expected bytes; wrong key and engine hash
were rejected. Flat layout was emulated from a genuine restic repository; this
test does not run the deployed gateway, VSS, enrollment or an installed agent.
An initial whole-snapshot restore hit an ancestor Windows ACL permission error;
the passing procedure uses the documented subtree selector. Existing key-export
PowerShell self-tests also passed for synthetic DPAPI envelopes and a loopback
test vault. These are local automated checks, not a completed external pilot
or real fleet qualification.

An invitation may say: “Try Stowline with 2–3 Windows test computers and
synthetic files on your own storage. We want to observe setup through a
verified restore, recovery without the original device, and an update that
keeps enrollment. Please record where help was needed and whether you would
continue using it.” Obtain authorization before sending invitations.
