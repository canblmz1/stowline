# Deploying Stowline

You need:

- a Linux server reachable from the office computers over HTTPS (a domain
  name pointing at it, or at least a public IP — see the Caddy note below);
- storage for the backups that [rclone](https://rclone.org) can write to
  (Google Drive, S3, B2, SFTP, a local disk...);
- nothing else for the computers: they get the installer from the GitHub
  releases (building it yourself needs Windows, Go 1.26+ and Python 3.12).

The server runs three services: the **control plane** (API + admin panel +
PostgreSQL), its **worker**, and the **storage gateway** (restic's REST API,
writing through rclone). **Only the reverse proxy (Caddy) may be reachable
from outside**: the control plane and gateway trust `X-Forwarded-*` headers
and the gateway's `/admin` API is protected by a token only.

## 1. Storage: an rclone remote

On any machine, run `rclone config` and create a remote for your storage
(e.g. `mydrive` of type `drive`). Setting a configuration password is
recommended (it encrypts `rclone.conf`); you then pass it as
`RCLONE_CONFIG_PASS`. Copy the resulting `rclone.conf` to the server.

Repositories go under the path you choose, e.g. `mydrive:stowline-repositories`;
each computer gets `<department>/<hostname>--<id>/...` inside it.

Run **one gateway instance with one worker per repository root**, as in the
provided Compose/systemd deployments. All writes to that root must go through
that gateway. RcloneStore serializes each object's metadata check, upload and
byte verification with a host-local OS file lock, including competing Python
processes that share the same `STOWLINE_GATEWAY_LOCK_DIR` and identical remote
root. Compose uses `/data/write-locks`; systemd uses
`/opt/stowline/var/gateway/write-locks`. Keep this directory writable and do not
delete its files while the gateway is running. The rclone subprocess ceiling
is per process, and the registry is intended for the single worker model.

Lock identity folds path case so accepted upper/lowercase hex spellings that
reach the same object on a case-insensitive backend cannot bypass each other.
Stored paths and existing repositories are unchanged. Drain writes and restart
the gateway when installing this fix; do not leave old and new writer processes
running together, since old processes still calculate the previous lock keys.

Independent gateway hosts/containers, different lock directories or aliases
of the same remote, and external writers are **not supported concurrent
writers**. These locks are not distributed locks. Rclone `--ignore-existing`
can report success without creating a file and does not provide an atomic
create guarantee across writers. The gateway verifies the stored bytes before
reporting a new write successful; uncertain metadata/transport/auth/JSON errors
stop a write with an error instead of being treated as an empty repository.
This does not add storage-level immutability or protect against an administrator
with the storage credentials. See the pinned rclone
[stat implementation](https://github.com/rclone/rclone/blob/v1.75.0/fs/operations/lsjson.go)
and [exit codes](https://rclone.org/docs/#list-of-exit-codes).

## 2. Sites and departments

Copy `deployments/sites.example.json` and edit it: one entry per location
with its own internet line. The important fields:

| field | meaning |
|---|---|
| `display_name` | shown in the panel and installer |
| `timezone`, `eligibility_start`, `preferred_latest_start`, `hard_stop` | when backups may start / must stop (local time) |
| `max_concurrent_wan_backups` | how many computers of this site may upload at once |
| `measured_upload_mbps`, `budget_percent` | starting values for the upload budget (also editable in **Settings**) |

A site without a measured upload speed admits **no** backups until one is
entered — zero is never treated as unlimited. Site ids are permanent;
editing the file later changes schedules but does not rename or delete sites
already in the database.

## 3a. Docker Compose (simplest)

The compose file uses the published images
`ghcr.io/<STOWLINE_IMAGE_OWNER>/stowline-server` and `-gateway`; add
`--build` to build them from the source tree instead.

```bash
cd deployments/compose
cp .env.prod.example .env      # fill in every value
cp /path/to/rclone.conf ./rclone.conf
cp ../sites.example.json ./sites.json   # and set SITES_FILE=./sites.json in .env
docker compose -f docker-compose.prod.yml up -d
```

Caddy obtains the HTTPS certificate for `STOWLINE_DOMAIN` automatically
(ports 80 and 443 must be open). Open `https://<domain>` and sign in with
`STOWLINE_ADMIN_USERNAME` / `STOWLINE_ADMIN_PASSWORD`.

## 3b. systemd on Debian/Ubuntu

Create the environment files (mode 0600, owned by root):

`/etc/stowline/control.env` (read by the control plane and the worker)

```ini
STOWLINE_PRODUCTION=1
STOWLINE_LAB_MODE=false
STOWLINE_ALLOW_INSECURE_HTTP=false
STOWLINE_CONTROL_HOST=127.0.0.1
STOWLINE_CONTROL_PORT=18180
STOWLINE_DATABASE_URL=postgresql+psycopg://stowline:<db password>@127.0.0.1:5432/stowline
STOWLINE_SECRET_KEY=<random>
STOWLINE_ADMIN_USERNAME=admin
STOWLINE_ADMIN_PASSWORD=<random>
STOWLINE_ESCROW_KEY=<Fernet key, optional: enables the Key Vault>
STOWLINE_ORG_NAME=My Organization
STOWLINE_SITES_FILE=/etc/stowline/sites.json
STOWLINE_GATEWAY_ADMIN_URL=http://127.0.0.1:18181
STOWLINE_GATEWAY_ADMIN_TOKEN=<random, same as in gateway.env>
STOWLINE_GATEWAY_PUBLIC_BASE=rest:https://<your domain>
```

`/etc/stowline/gateway.env`

```ini
STOWLINE_GATEWAY_ROOT=rclone:mydrive:stowline-repositories
STOWLINE_GATEWAY_HOST=127.0.0.1
STOWLINE_GATEWAY_PORT=18181
STOWLINE_GATEWAY_ADMIN_TOKEN=<same random token>
STOWLINE_GATEWAY_DATA=/opt/stowline/var/gateway
STOWLINE_GATEWAY_REGISTRY=/opt/stowline/var/gateway/registry.json
RCLONE_CONFIG=/opt/stowline/var/gateway/rclone.conf
RCLONE_CONFIG_PASS=<only if rclone.conf is encrypted>
```

`/etc/stowline/dbdump.env`

```ini
STOWLINE_DB_PASSWORD=<db password>
# optional: also copy the nightly database dump here
STOWLINE_DBDUMP_REMOTE=mydrive:stowline-db-backups
```

Put `rclone.conf` at `/opt/stowline/var/gateway/rclone.conf` and your sites
file at `/etc/stowline/sites.json`, install Caddy, then from the source tree:

```bash
sudo bash deployments/systemd/install.sh
sudo bash deployments/systemd/caddy-enable.sh backup.example.com
```

`install.sh` creates the `stowline` user, a loopback-only PostgreSQL
database, private Python 3.12 environments, the pinned rclone, and the
`stowline-control`, `-worker`, `-gateway` units plus a nightly
`stowline-dbdump` timer. Re-run it to upgrade.

*No domain?* Use the public IP as the site address; Caddy can get a
Let's Encrypt IP certificate with the short-lived profile (see the comment in
`deployments/systemd/stowline.caddy`).

## 4. Check the site settings

In the panel, **Settings → Sites**: enter each site's measured upload speed
(Mbps) and the share backups may use. New backups are admitted only within
that budget.

## 5. The installer

Download `Stowline-Setup-<version>.exe` from the project's GitHub releases
and give it to whoever sets up the computers, together with a **setup
code** from the panel (**Settings → Setup codes**: valid for a chosen number
of days and computers, optionally for one site only, revocable). The code
can only enroll computers and name/configure the ones it enrolled -- it
cannot sign in to the panel -- so the admin password is never typed on the
PCs. The installer still accepts the admin login instead. It asks for your server
address on its first screen and takes the sites and departments from the
server (the public, non-secret `GET /api/v1/setup/catalog`). It needs
administrator rights and opens the setup wizard for a fresh installation.
The updated launcher stops if Stowline is already installed; use the
enrollment-preserving agent update procedure below. The exe is not code-signed, so Windows
SmartScreen warns once ("More info" → "Run anyway") unless you sign it with
your own certificate.

To build it yourself, or to build one with your server address fixed in it
(the wizard then never asks), on Windows from the source tree:

```powershell
.\scripts\build-release.ps1 -OutDir releases\build
python scripts\pin-release.py --agent releases\build\stowline-agent.exe --version 0.1.1
python scripts\pin-release.py --verify
.\scripts\fetch-pinned-binaries.ps1
python scripts\package-installer.py [--server https://backup.example.com --sites C:\path\to\sites.json]
```

The result is `releases\setup-single\Stowline Setup.exe`.

## 6. Agent updates

The release workflow builds a versioned Windows agent, publishes its binary
and matching SHA-256 manifest, and packages those same bytes in the server
image. New builds have **`agent_qualified: false`**. CI and synthetic engine
tests do not qualify a real Windows installation.

Stage a local candidate without qualifying it:

```powershell
python scripts\pin-release.py --agent releases\build\stowline-agent.exe --version 0.1.1
python scripts\pin-release.py --verify
```

Use the [release/update procedure](RELEASE-UPDATES.md) to deploy the bundle,
select explicit canary device IDs, verify preserved identity/history and
exercise busy backup, busy restore and rollback. The public `v0.1.1` agent
does not understand the new canary permission: its first transition requires
the documented service-stop/binary-only bootstrap after running work finishes.
Do not uninstall or re-enroll to update it. Previously downloaded installers
retain their original behavior; use the corrected build for fresh installations.

Only after real Windows checks and review of the exact candidate's evidence:

```powershell
python scripts\pin-release.py --qualify --qualification path\to\report.json
```

Deploy the promoted manifest with its matching binary. Updated agents verify
the downloaded hash and defer restart during backup/restore. Rollback is a
failed-boot guard, not a general health timeout; a candidate that stays alive
without heartbeats or fails before executing its guard can require manual
rollback. The procedure documents those limits. Agent update does not update
restic/rclone, desktop applications or enroll a second device.

For a control-plane-only systemd redeploy, `STOWLINE_RESTART_GATEWAY=0 install.sh`
leaves gateway uploads running. The recovery-location change requires updating
both the panel and gateway; the guide reports missing information against an
older gateway rather than guessing a path.

## Backing up the backup keys

Every computer's repository is encrypted with a key that exists only on
that computer (DPAPI-protected). If the computer is lost, so is access to its
backups — unless the key was saved elsewhere:

- the **Key Vault** (needs `STOWLINE_ESCROW_KEY`): an encrypted copy on the
  server, revealed only after re-entering the admin password, every reveal
  audited; and/or
- `scripts/key-escrow/` — a tool to run on the computer that shows the key
  once for a password manager / paper copy and can send it to the Key Vault.

Keep `STOWLINE_ESCROW_KEY` itself somewhere safe and separate: without it the
Key Vault copies cannot be decrypted.

The authorized recovery guide obtains the actual root, enrollment-time storage
prefix and generation from the gateway registry. Renaming a department or
computer does not move that repository. Preserve the registry with your private
deployment backups. On a clean workstation configure the same named rclone
remote separately; a local store path is on the gateway/container and needs an
export there first. An unavailable or ambiguous location must be resolved before
recovery. Existing repositories do not move as part of this change.

Follow [the pilot and independent recovery procedure](PILOT.md) to compare
restored bytes without the original device and retain database/config/escrow
recovery material. Backup success, off-device key custody and verified recovery
are separate outcomes. Retention/prune and storage capacity remain the IT
operator's responsibility; configured retention values do not schedule pruning.
