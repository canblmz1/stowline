# Release agents and existing-device updates

The tag release builds a Windows agent whose `version` output matches the tag,
then generates `release/manifest.json` from those exact bytes. The release
publishes the standalone agent, manifest and checksums alongside the installer.
The server image receives that same manifest and agent bundle from the Windows
build job; it does not build or select a different agent. The installer refuses
an agent that differs from the release pin. `SHA256SUMS.txt` detects accidental
corruption; it is not a separate signature or a trust source independent of the
release publisher. Keep control-plane access and HTTPS protected.

Every newly built release has **`agent_qualified: false`**. Building on a Windows
CI runner, passing mock tests, or running an agent's `version` command does not
qualify the Windows service, VSS, a real device update, or independent recovery.
The committed manifest can have an empty agent pin before a release build.

The public 0.1.1 agent predates the separate `agent_canary_allowed` heartbeat
field. It will ignore an unqualified canary from the corrected server. Bootstrap
that older agent once using the manual procedure below. Never set
`agent_qualified: true` just to get an untested canary onto an old agent.

## Existing installations

1. Record the existing device ID, generation/repository binding, snapshots and
   agent hash. Keep recovery keys outside the device before testing an update.
2. Stage the candidate using the release bundle. To stage a local Windows build:

   ```powershell
   ./scripts/build-release.ps1 -OutDir releases/build -Version 0.1.1
   python scripts/pin-release.py --agent releases/build/stowline-agent.exe --version 0.1.1
   python scripts/pin-release.py --verify
   ```

3. Test on explicitly selected, disposable Windows devices first. Place the
   candidate at `release/bin/stowline-agent-canary.exe` and add this optional
   manifest entry, using the candidate's actual SHA-256 and server-assigned IDs:

   ```json
   "canary": {
     "agent_sha256": "<64 lowercase hex characters>",
     "device_ids": ["<selected-test-device-id>"]
   }
   ```

   These IDs are an explicit opt-in to an **unqualified** candidate. Only the
   selected devices receive its pin and binary; other devices keep the normal
   pin. Remove the entry to end the canary. This canary entry deliberately opts
   those devices into testing; it does not mark the general release qualified.
   Mount/deploy the changed release directory into the server; editing a local
   checkout does not change an already-running image.
4. Verify the same device ID, repository binding, keys and snapshot history
   before and after the update. Run a synthetic backup, compare restored file
   bytes, and restore again from a clean machine without the original endpoint.
   Start backup and restore separately during a pending update and confirm each
   finishes before restart. Test a candidate that fails self-check and one that
   repeatedly fails to reach a successful heartbeat; verify the prior binary
   returns after the boot guard and that the next heartbeat identifies it.
5. Only after reviewing real Windows evidence, promote the exact candidate:

   ```powershell
   python scripts/pin-release.py --qualify --qualification path/to/report.json
   ```

   Deploy/mount the promoted manifest and its matching binary to enable the
   general update. Keep the report and referenced evidence in a private release
   record. The manifest stores only the report hash, not device or operator
   details. Do not publish credentials, backup keys or raw configuration in it.

The agent downloads only a qualified normal pin or its explicitly selected
canary. It verifies SHA-256, runs the staged binary's `version` self-check, keeps
the previous executable, and writes a pending marker before SCM restart. A
shared admission gate defers staging while backup/restore is active and prevents
a new operation from starting during download, swap and restart. A failed or
no-op update reopens admission. The update changes the agent executable only;
it does not invoke enrollment or rewrite configuration, keys, repository or
journal. Restic/rclone and desktop applications are not updated by this path.

A successful heartbeat confirms the new binary. More than three service starts
without one restore the saved binary on the fourth start. This is a boot-loop
guard, not a general health monitor: a process that remains alive but cannot
heartbeat is not automatically timed out, and the new binary must start far
enough to execute the guard. An operator may need to stop the service and put a
saved rollback executable back. Do not delete rollback copies during a canary.

The startup guard runs inside the registered SCM handler, before configuration,
secrets, engine preparation or background work. On rollback the handler reports
the restart handoff synchronously, then terminates without `SERVICE_STOPPED` so
the verified SCM restart action starts the restored executable. It does not
depend on recovery for graceful nonzero stops or a host-wide timeout change.
Application preparation failures after recovery verification use the same
abnormal termination, so subsequent guarded starts can reach the rollback limit.
Install and installed-service startup apply and read back the 5-second restart
action and 24-hour reset period. A recovery configuration/read-back failure is
reported to Event Log/stderr and stops startup before automatic updates or
application work are admitted. Interactive `service run` does not configure the
installed service. A successful SCM restart and subsequent heartbeat must still
be verified for each exact candidate; restoring bytes alone is insufficient.

Use this update path for enrolled devices. The installer is for initial setup
only: both the graphical launcher and `Setup.cmd` preserve an existing
installation and stop. They do not remove another server's install or offer a
reset as an update. When directly running the repair wizard, a valid existing enrollment is reused;
an unverified, revoked, or different-server enrollment stops with an error
instead of silently creating another device. Repair access first. Removing
Stowline and enrolling again intentionally creates a new identity and is not an
update; retain the old repository and recovery materials when doing that.

## First update of an older agent

Use a maintenance window for one disposable Windows test device first. Before
stopping its service, wait for backup and restore to finish, pause new backup
requests/scheduling in the panel, and have users keep the local restore app
closed. Verify the device and job state in the panel. The older agent cannot
atomically coordinate a manual file replacement with new work; the operator
must hold new work for this maintenance window. Do not stop an active job to
make room for the update. The new agent's admission gate applies to subsequent
automatic updates.

Download the manifest and standalone agent from the same release into a local
directory. From administrator PowerShell, with the real release directory:

```powershell
$candidateDir = 'C:\Temp\stowline-release'
$manifest = Get-Content -LiteralPath "$candidateDir\manifest.json" -Raw | ConvertFrom-Json
$candidate = Join-Path $candidateDir 'stowline-agent.exe'
$live = 'C:\Stowline\bin\stowline-agent.exe'
if ($manifest.agent_sha256 -notmatch '^[0-9a-f]{64}$') { throw 'Missing agent pin' }
if ((Get-FileHash -LiteralPath $candidate -Algorithm SHA256).Hash.ToLower() -ne $manifest.agent_sha256) { throw 'Agent hash mismatch' }
if ((& $candidate version) -ne "stowline-agent $($manifest.version)" -or $LASTEXITCODE -ne 0) { throw 'Agent version/self-check failed' }
$configBefore = (Get-FileHash -LiteralPath 'C:\Stowline\config\pilot.json' -Algorithm SHA256).Hash
$savedAgent = "$live.rollback-manual-$(Get-Date -Format yyyyMMddHHmmss)"
Stop-Service StowlineBackup -ErrorAction Stop
(Get-Service StowlineBackup).WaitForStatus('Stopped', [TimeSpan]::FromSeconds(60))
Copy-Item -LiteralPath $live -Destination $savedAgent -ErrorAction Stop
Copy-Item -LiteralPath $candidate -Destination $live -Force -ErrorAction Stop
if ((Get-FileHash -LiteralPath $live -Algorithm SHA256).Hash.ToLower() -ne $manifest.agent_sha256) { throw 'Installed agent hash mismatch; restore saved agent' }
if ((Get-FileHash -LiteralPath 'C:\Stowline\config\pilot.json' -Algorithm SHA256).Hash -ne $configBefore) { throw 'Configuration changed; investigate before starting' }
Start-Service StowlineBackup -ErrorAction Stop
```

This replaces only the executable. Do not run `config write`, `control enroll`,
`Remove.cmd`, or the graphical installer. Confirm the original device ID,
repository, keys and history remain intact, verify the reported new agent hash,
then run backup and byte-verified restore. Do not mark the build qualified yet;
complete the real-device checks above first. If it fails, stop the service,
copy `$savedAgent` back to `$live`, and start the service. Verify the original
hash and heartbeat. Manual bootstrap has no new pending-upgrade marker, so this
first rollback is an operator action. Keep the saved binary until verification
is complete.

## Qualification record

The report is an operator attestation, not a cryptographic proof that a test
happened. The tool rejects missing, failed, wrong-platform, wrong-version,
wrong-hash, or changed evidence. Review of the actual Windows results remains
the release operator's responsibility. Synthetic test fixtures exercising this
validator do not count as qualification.

Report fields:

| Field | Required value |
| --- | --- |
| `schema` | `stowline.windows-release-qualification.v1` |
| `version`, `agent_sha256` | Exact staged version and binary hash |
| `platform` | `windows` |
| `windows_version`, `device_id`, `operator` | Actual Windows build, test device ID and reviewer |
| `checks` | An object with every check below |

Each check must contain `result: "PASS"`, a relative `evidence_file` within the
report directory, and that nonempty file's SHA-256 in `evidence_sha256`:

- `backup_restore_bytes`: source/restored hashes and actual engine results.
- `independent_restore_bytes`: clean-host recovery and compared bytes.
- `enrollment_preserved`: before/after identity, repository and snapshot evidence.
- `busy_backup_deferred`: backup completion before update restart.
- `busy_restore_deferred`: restore completion before update restart.
- `unhealthy_rollback`: failed-candidate and restored-binary/service evidence.

CI always stages unqualified builds. Promotion is a separate operator action;
the tag workflow neither runs this real-device procedure nor claims it passed.

## Prepare one exact qualification candidate

After the narrow fixes and normal PR checks pass, freeze the source commit and
run the existing CI workflow without publishing a release or image:

```powershell
gh workflow run ci.yml --ref fix/operator-recovery-chain -f candidate_version=0.1.2-rc.1
```

The `windows-qualification-candidate` Actions artifact contains the single-file
installer, portable installer zip, agent, recovery tool, manifest,
`server-release.zip`, checksums and `candidate.json`. Download it before its
30-day retention expires and keep the exact bytes with the qualification record.
The record binds the source commit, version, agent SHA-256 and every artifact's
hash. It records full-installer tests separately from Windows qualification.
The four previously optional embedded-runtime/installer tests must actually pass
without skips for a candidate to be emitted; packaged bootstrap is also run in
`--verify-only` mode, which does not install a service or enroll a device.

The image job consumes this same candidate's manifest and binary and checks
their hash/version inside the Linux server image. It does not push the image.
Keep that job's successful run link together with the candidate record; the
record itself is generated before the image job finishes. Do not rebuild an
agent between qualification and promotion: a different hash is a new candidate.
`agent_qualified` remains false throughout candidate preparation.

## Installed Windows qualification checklist

Use only explicitly authorized, disposable Windows hosts, a separate control
plane/database/gateway root and synthetic files. A developer workstation or CI
runner running unit tests is not an installed-device qualification environment.
No such host was authorized for the second PR implementation round; the steps
below remain **NOT RUN** until that environment is supplied.

Verify `candidate.json`, the source commit and `SHA256SUMS.txt` before starting.
Use a known baseline agent that understands `agent_canary_allowed` to test
automatic updates; the public 0.1.1 agent needs the earlier manual bootstrap.
Keep its executable/hash and all recovery material. Select only the test device
ID in the isolated server's canary entry. Never change qualification to true
to bypass eligibility.

| Check | Required observation/evidence |
| --- | --- |
| Service/account/heartbeat | Install from the candidate on a fresh host; record `sc.exe qc StowlineBackup`, actual service account and Windows build. Start/restart through SCM; observe the same device's heartbeat and candidate SHA. |
| Existing enrollment | On an enrolled baseline host, record device/installation/repository/generation, key fingerprint and snapshot history. Update only the executable; verify all bindings, old snapshot restore and device count afterwards. Keep secrets out of evidence. |
| Backup/VSS | Back up synthetic text/binary/empty files and an application-appropriate open/locked-file case. Record actual VSS results and compare restored bytes with a retained inventory. A successful restic local test does not prove VSS/application consistency. |
| Busy backup | Offer the exact candidate during an active backup. Record old executable hash/service PID throughout the job, terminal result, subsequent restart and new heartbeat. |
| Restore completion | Offer during restore. Check the initiating Windows user's actual read access, then terminal state on disk and after restart. Record engine completion, ACL grant, state persistence and restart order separately. |
| Wrong/corrupt artifact | Use disposable candidate copies. A mismatched published hash/file or corrupted download must be rejected; current executable, identity and repository remain intact. Record server/agent rejection. |
| Unhealthy/rollback | On the isolated host, prevent the updated service from confirming a healthy heartbeat, exercise the failed-boot guard and verify the restored executable plus subsequent heartbeat. Observe the behavior while the same bad pin remains offered, then withdraw it. Keep a manual recovery path. |
| Full installer | Retain the packaged runtime test report and additionally exercise the actual installer/UAC/first enrollment on a clean host without developer Python/Go. Existing installations must stop without deleting identity, keys or history. |

Two existing design limits need explicit handling; they were not introduced by
this PR's three narrow fixes:

- The restore engine's activity gate currently ends before self-service
  `GrantRead` and the final job-state persistence. There is no installed-service
  evidence that automatic restart waits through those final steps. For the pilot,
  withdraw the canary/normal update offer before a user restore, wait for a
  heartbeat acknowledging no eligible update, and re-offer only after the user's
  access and terminal state are verified. Do not qualify concurrent automatic
  restore/update behavior from an engine-only test. If the controlled check
  restarts early, retain the failure evidence and either fix the boundary or
  keep updates in an explicit restore-free maintenance window.
- Rollback does not quarantine a failed SHA. A restored agent that understands
  the canary protocol may retry the same still-offered pin. Before returning a
  host to ordinary work, withdraw that offer (or restore the known-good normal
  pin), verify a heartbeat sees the changed policy, and confirm that restart/
  download attempts stop. A rollback by itself is not proof of a stable rollout.

Keep PASS/FAIL/NOT RUN per check. Missing test environment is NOT RUN, never
PASS. Review the exact candidate's real-device evidence before promotion and
before treating a 2–3 device independent-operator pilot as complete.

The previous synthetic whole-snapshot ACL test's temporary directory still
existed when this round began. Only its existence was checked; the previously
rejected ACL modification/cleanup was not retried.
