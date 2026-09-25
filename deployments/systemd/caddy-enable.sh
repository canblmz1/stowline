#!/usr/bin/env bash
# Put Stowline behind an existing Caddy:  bash caddy-enable.sh backup.yourcompany.com
# Installs stowline.caddy for that domain and appends one `import` line to
# /etc/caddy/Caddyfile; validates before a graceful reload and rolls back on
# any failure (Caddy keeps the old config running if the new one fails).
set -euo pipefail
DOMAIN=${1:?usage: caddy-enable.sh <domain or IP>}
CF=/etc/caddy/Caddyfile
SNIPPET=/etc/caddy/stowline.caddy
LINE="import $SNIPPET"

sed "s/^backup\.example\.com {/$DOMAIN {/" "$(dirname "$0")/stowline.caddy" > "$SNIPPET.new"
install -m 0644 "$SNIPPET.new" "$SNIPPET" && rm -f "$SNIPPET.new"
touch "$CF"
backup="$CF.bak.$(date -u +%Y%m%d%H%M%S)"
cp -a "$CF" "$backup"

if ! grep -qxF "$LINE" "$CF"; then
  printf '\n# Stowline (see %s) -- added by deployments/systemd/caddy-enable.sh\n%s\n' "$SNIPPET" "$LINE" >> "$CF"
fi

rollback() {
  echo "rolling back Caddyfile from $backup"
  cp -a "$backup" "$CF"
  systemctl reload caddy || true
  exit 1
}

# Validate as the caddy user: validating as root opens the log file as root
# (0600), and the real caddy process then cannot open it on reload.
install -d -o caddy -g caddy -m 0755 /var/log/caddy
[[ -e /var/log/caddy/stowline.access.log ]] || install -o caddy -g caddy -m 0600 /dev/null /var/log/caddy/stowline.access.log
runuser -u caddy -- caddy validate --config "$CF" --adapter caddyfile >/dev/null 2>&1 || { runuser -u caddy -- caddy validate --config "$CF" --adapter caddyfile 2>&1 | tail -5; rollback; }
systemctl reload caddy || rollback
sleep 2
systemctl is-active --quiet caddy || rollback
curl -fs http://127.0.0.1:2019/config/ | grep -q "$DOMAIN" || { echo "Stowline site block missing from the live config"; rollback; }
echo "caddy reloaded with Stowline on $DOMAIN; previous Caddyfile: $backup"
