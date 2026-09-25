# Demo

A made-up office to click around in: eight Windows computers on two sites,
a backup history, and a few backups running live, all fake. You don't need
Windows PCs or a storage account.

```bash
docker compose up -d
```

Open <http://localhost:8080> and sign in as **admin / demo**. The panel
follows your browser's language; the TR/EN switch is at the top.

- `seed.py` enrolls the fake computers through the same API the real agent
  uses, then keeps a few of them "backing up" within each site's upload
  budget. Nothing touches your files.
- Stop with `docker compose down`; start over with `docker compose down -v`.
- It uses the published images (`latest`); set `STOWLINE_VERSION=0.1.0`
  to pin a release.

This is a lab setup: plain HTTP, lab mode, a known password, bound to
127.0.0.1. Never put real data in it — see [docs/DEPLOYMENT.md](../../docs/DEPLOYMENT.md)
for a real server.
