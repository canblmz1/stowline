# Architecture

## Components

**Agent** (`agent/cmd/stowline-agent`, Go, Windows service `StowlineBackup`,
runs as LocalSystem, installed in `C:\Stowline`)
- runs restic with VSS (`--use-fs-snapshot`) on the selected folders, with
  per-run bandwidth limits and a stall watchdog;
- schedules daily backups inside the site's window, catches up missed ones,
  retries failures a bounded number of times;
- talks to the control plane: heartbeat, command claim/ack (backup now,
  restore to staging, browse, apply folder selection, catalog...), WAN
  admission leases (renewed every 10 s with live progress);
- serves the local UI on `127.0.0.1:18080` for the desktop app (Host/Origin
  checks, POSTs need `X-Stowline-Local: 1`). Each request is tied to the
  Windows account of the process that opened the connection (TCP table →
  process → token); that account sees, searches and restores only its own
  profile and folders outside every profile, never another account's
  `C:\Users\<name>`, and only it gets read access to what it restored;
- upgrades itself when the server pins a qualified new build.

**Desktop programs** (Go + WebView2): `Stowline Setup.exe` (installer shell;
can embed the whole package), `Stowline Backups.exe` (user app on top of the
local UI: native folder picker, delivers restores into *Restored Files*),
`Stowline Admin.exe` (opens the admin panel in its own window).

**Setup wizard** (`tools/stowline-setup`, Python/FastAPI in an embedded
runtime, runs elevated during install on `127.0.0.1` with a per-run token):
checks connectivity and the package pins, discovers work files, compiles the
folder selection, enrolls the computer with the admin's credentials,
initialises its repository, provisions secrets and installs the service.

**Control plane** (`server/`, FastAPI + PostgreSQL, plus `stowline-worker`)
- admin panel (plain JS in `server/app/static`), audit log, users' messages;
- devices, enrollment tokens, policies, selections, commands, attempts,
  snapshot catalogs (file lists kept server-side for browsing while PCs are
  off), restore requests;
- **WAN admission**: per site, at most `max_concurrent_wan_backups` leases;
  each lease gets a share of the site's budget; leases expire unless renewed;
- the worker expires commands, updates health, reaps leases and pushes each
  site's upload cap to the gateway every minute.

**Gateway** (`gateway/`, FastAPI): restic's REST protocol under
`/restic/<device>/<generation>/...`, authenticated per device (credentials
registered by the control plane), confined to the device's storage prefix,
stored through rclone. A token bucket per site caps total upload bytes/s.

**Recovery tool** (`recovery/`): restores a repository copy without the
server (e.g. after copying it out of the cloud with rclone).

## Data flow of a backup

1. The agent's scheduler (or a "back up now" command) asks for a WAN lease.
2. The control plane admits it if the site has capacity and budget; the
   lease carries the bandwidth for this run.
3. restic reads a VSS snapshot of the folders and writes encrypted packs to
   the gateway (`rest:https://<server>/restic/...`), which streams them to
   storage via rclone.
4. The agent renews the lease with progress; the panel shows it live.
5. The result (snapshot id, counts, errors) is reported and the command
   acked; a file catalog of the snapshot is uploaded for the panel.

## Security model

- **Encryption**: restic encrypts everything before it leaves the PC. The
  repository password is generated per computer and stored with DPAPI
  (machine scope) under `C:\Stowline\secrets`; the server never receives it
  unless the operator uses the Key Vault.
- **No storage credentials on endpoints**: PCs authenticate to the gateway
  with a per-device credential; only the gateway holds `rclone.conf`.
- **Least privilege per device**: the gateway maps each credential to its
  own storage prefix; a PC cannot read or list another PC's repository.
- **Admin panel**: session cookies (Secure outside lab mode), CSRF token and
  origin checks, login rate limiting, audit log for every operator action
  and every key reveal.
- **Installer integrity**: the package pins the SHA-256 of every binary it
  installs; the wizard and bootstrap refuse mismatches. The agent verifies
  an upgrade's SHA-256 against the server's pin and self-checks it before
  switching.
- **Restores never overwrite**: restores go to a staging folder; the desktop
  app then copies the picked items into a new dated *Restored Files* folder.
- **Safe defaults**: the server refuses to start with the built-in dev
  password/secret unless lab mode is explicitly on; lab mode is off by
  default.

Known limits: the executables are not code-signed; there is no SSO (one
local admin account); restic `forget`/`prune` (retention cleanup) is not run
automatically — repositories grow until you prune them yourself.
