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
administrator rights; it unpacks itself, removes a Stowline install that
belongs to another server, asks before replacing one that belongs to this
one, and opens the setup wizard. The exe is not code-signed, so Windows
SmartScreen warns once ("More info" → "Run anyway") unless you sign it with
your own certificate.

To build it yourself, or to build one with your server address fixed in it
(the wizard then never asks), on Windows from the source tree:

```powershell
.\scripts\build-release.ps1 -OutDir releases\build
.\scripts\fetch-pinned-binaries.ps1
python scripts\package-installer.py [--server https://backup.example.com --sites C:\path\to\sites.json]
```

The result is `releases\setup-single\Stowline Setup.exe`.

## 6. Agent updates

After testing a new agent build on one computer:

```powershell
python scripts\pin-release.py --agent releases\build\stowline-agent.exe --qualify
```

then redeploy the server (`install.sh` again, or rebuild the compose
images; `STOWLINE_RESTART_GATEWAY=0 install.sh` leaves the gateway, and the
uploads going through it, alone when only the control plane changed).

To try a new build on a few computers first, put it at
`release/bin/stowline-agent-canary.exe` and add to `release/manifest.json`

```json
"canary": {"agent_sha256": "<its SHA-256>", "device_ids": ["<device id>", "..."]}
```

Only those computers upgrade (the file is re-read on every heartbeat, no
restart needed); the rest keep the qualified pin. Remove the entry to stop. Every enrolled computer picks the new build up on its next
heartbeat, verifies the SHA-256, restarts into it once no backup or restore
is running, and rolls back if it does not come up healthy.

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
