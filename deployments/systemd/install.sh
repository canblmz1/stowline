#!/usr/bin/env bash
# Install/upgrade Stowline on a Debian/Ubuntu server. Run as root from the
# source (or release) directory:  bash deployments/systemd/install.sh
# then put it behind Caddy:        bash deployments/systemd/caddy-enable.sh <domain>
#
# Adds only /opt/stowline, /etc/stowline, the stowline-* units, a stowline
# system user and PostgreSQL (loopback only). All services listen on
# 127.0.0.1; Caddy is the only public entry. No Docker, no firewall changes.
#
# Secrets are NOT passed here: /etc/stowline/{control,gateway,dbdump}.env
# and /opt/stowline/var/gateway/rclone.conf must already exist (0600) --
# see docs/DEPLOYMENT.md for their contents.
set -euo pipefail

SRC=$(cd "$(dirname "$0")/../.." && pwd)
BASE=/opt/stowline
REL=$(date -u +%Y%m%dT%H%M%SZ)
RCLONE_VER=1.75.0
RCLONE_ZIP_SHA256=aa2804e08f48250e71009c727124b6341cd0288465804a9a09d14663cabafbaa
UV_VER=${UV_VER:-0.12.18}

log() { echo "[stowline] $*"; }

for f in control.env gateway.env dbdump.env; do
  [[ -f /etc/stowline/$f ]] || { echo "missing /etc/stowline/$f"; exit 1; }
done
[[ -f $BASE/var/gateway/rclone.conf ]] || { echo "missing $BASE/var/gateway/rclone.conf"; exit 1; }

# --- user and directories -------------------------------------------------
id stowline >/dev/null 2>&1 || useradd --system --home-dir "$BASE" --shell /usr/sbin/nologin stowline
install -d -o stowline -g stowline -m 0750 "$BASE" "$BASE/var" "$BASE/var/control" "$BASE/var/gateway" "$BASE/var/pgdump"
install -d -o root -g root -m 0755 "$BASE/app" "$BASE/bin"
chown -R stowline:stowline "$BASE/var"
chmod 0600 "$BASE/var/gateway/rclone.conf"
chown root:stowline /etc/stowline /etc/stowline/*.env
chmod 0750 /etc/stowline
chmod 0640 /etc/stowline/*.env

# --- application code ---------------------------------------------------------
dest="$BASE/app/$REL"
mkdir -p "$dest"
cp -a "$SRC/server" "$SRC/gateway" "$SRC/release" "$dest/"
mkdir -p "$dest/deployments"
cp -a "$SRC/deployments/systemd" "$dest/deployments/"
chmod 0755 "$dest/deployments/systemd/"*.sh
ln -sfn "$dest" "$BASE/app/current"
# keep the three newest releases
ls -1dt "$BASE/app"/2* 2>/dev/null | tail -n +4 | xargs -r rm -rf

# --- PostgreSQL (loopback only; Ubuntu's default listen_addresses=localhost) --
if ! command -v psql >/dev/null; then
  log "installing postgresql"
  DEBIAN_FRONTEND=noninteractive apt-get update -q >/dev/null
  DEBIAN_FRONTEND=noninteractive apt-get install -y -q postgresql
fi
systemctl enable --now postgresql >/dev/null
# shellcheck disable=SC1091
DBPW=$(grep '^STOWLINE_DB_PASSWORD=' /etc/stowline/dbdump.env | cut -d= -f2-)
sudo -u postgres psql -v ON_ERROR_STOP=1 -tAc "SELECT 1 FROM pg_roles WHERE rolname='stowline'" | grep -q 1 \
  || sudo -u postgres psql -v ON_ERROR_STOP=1 -c "CREATE ROLE stowline LOGIN PASSWORD '$DBPW'" >/dev/null
sudo -u postgres psql -v ON_ERROR_STOP=1 -tAc "SELECT 1 FROM pg_database WHERE datname='stowline'" | grep -q 1 \
  || sudo -u postgres createdb -O stowline stowline

# --- uv + Python 3.12 (private to /opt/stowline; the system Python is untouched)
if [[ ! -x $BASE/bin/uv ]]; then
  log "installing uv $UV_VER"
  tmp=$(mktemp -d)
  curl -fsSL "https://github.com/astral-sh/uv/releases/download/$UV_VER/uv-x86_64-unknown-linux-gnu.tar.gz" -o "$tmp/uv.tgz"
  curl -fsSL "https://github.com/astral-sh/uv/releases/download/$UV_VER/uv-x86_64-unknown-linux-gnu.tar.gz.sha256" -o "$tmp/uv.sha256"
  (cd "$tmp" && echo "$(cut -d' ' -f1 uv.sha256)  uv.tgz" | sha256sum -c -)
  tar -xzf "$tmp/uv.tgz" -C "$tmp"
  install -m 0755 "$tmp"/uv-x86_64-unknown-linux-gnu/uv "$BASE/bin/uv"
  rm -rf "$tmp"
fi
export UV_PYTHON_INSTALL_DIR=$BASE/python UV_CACHE_DIR=$BASE/.uv-cache
"$BASE/bin/uv" python install 3.12 >/dev/null
for v in control gateway; do
  [[ -x $BASE/venv-$v/bin/python ]] || "$BASE/bin/uv" venv --python 3.12 "$BASE/venv-$v" >/dev/null
done
"$BASE/bin/uv" pip install --python "$BASE/venv-control/bin/python" -q "$BASE/app/current/server[psycopg]"
"$BASE/bin/uv" pip install --python "$BASE/venv-gateway/bin/python" -q "$BASE/app/current/gateway"
chmod -R a+rX "$BASE/python" "$BASE/venv-control" "$BASE/venv-gateway"

# --- rclone (pinned; same build as deployments/compose/Dockerfile.gateway) ----
if [[ ! -x $BASE/bin/rclone ]] || ! "$BASE/bin/rclone" version | grep -q "v$RCLONE_VER"; then
  log "installing rclone $RCLONE_VER"
  tmp=$(mktemp -d)
  curl -fsSL "https://downloads.rclone.org/v$RCLONE_VER/rclone-v$RCLONE_VER-linux-amd64.zip" -o "$tmp/rclone.zip"
  echo "$RCLONE_ZIP_SHA256  $tmp/rclone.zip" | sha256sum -c -
  python3 -c "import zipfile,sys; zipfile.ZipFile(sys.argv[1]).extractall(sys.argv[2])" "$tmp/rclone.zip" "$tmp"
  install -m 0755 "$tmp/rclone-v$RCLONE_VER-linux-amd64/rclone" "$BASE/bin/rclone"
  rm -rf "$tmp"
fi

# --- systemd units -------------------------------------------------------------
for u in stowline-control.service stowline-worker.service stowline-gateway.service stowline-dbdump.service stowline-dbdump.timer; do
  install -m 0644 "$BASE/app/current/deployments/systemd/$u" "/etc/systemd/system/$u"
done
systemctl daemon-reload
systemctl enable stowline-gateway stowline-control stowline-worker stowline-dbdump.timer >/dev/null
# A gateway restart drops uploads in flight (and a setup wizard creating its
# repository at that moment fails). STOWLINE_RESTART_GATEWAY=0 keeps it
# running when only the control plane changed.
if [ "${STOWLINE_RESTART_GATEWAY:-1}" = "1" ]; then
  systemctl restart stowline-gateway
else
  echo "[stowline] gateway left running (STOWLINE_RESTART_GATEWAY=0)"
fi
systemctl restart stowline-control
systemctl restart stowline-worker
systemctl start stowline-dbdump.timer

for i in $(seq 1 30); do
  if curl -fs http://127.0.0.1:18180/health >/dev/null && curl -fs http://127.0.0.1:18181/health >/dev/null; then
    log "control and gateway healthy on loopback (release $REL)"
    exit 0
  fi
  sleep 2
done
echo "services did not become healthy"; systemctl --no-pager status stowline-control stowline-gateway | tail -30
exit 1
