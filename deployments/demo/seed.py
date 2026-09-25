"""Fill the demo server with a made-up office and keep it alive.

Runs inside the server image (it only needs httpx). It enrolls a few fake
computers through the same API the real agent uses, gives them a backup
history, then loops forever: computers send heartbeats, and a couple of them
at a time "back up" with live progress, finish, and hand the upload slot to
the next one. Nothing is read from or written to real computers.
"""
import json
import os
import random
import time
import uuid
from pathlib import Path

import httpx

BASE = os.environ.get("DEMO_SERVER", "http://server:8080")
PASSWORD = os.environ.get("STOWLINE_ADMIN_PASSWORD", "demo")
# The fake computers' credentials, so a restarted demo carries on.
STATE = Path(os.environ.get("DEMO_STATE", "/state/computers.json"))

PCS = [
    # hostname, display name, site, department, past backups, size of the backed-up data (GB)
    ("RECEPTION-01", "Reception", "hq", "Sales", 9, 6.2),
    ("ACC-MARIA", "Accounting - Maria", "hq", "Finance", 14, 18.4),
    ("ACC-JONAS", "Accounting - Jonas", "hq", "Finance", 12, 11.7),
    ("MGR-LAPTOP", "Manager laptop", "hq", "Management", 7, 24.9),
    ("HR-DESK", "HR desk", "hq", "HR", 10, 4.8),
    ("WORKSHOP-2", "Workshop terminal", "branch", "Service", 11, 9.2),
    ("SALES-TOM", "Sales - Tom", "branch", "Sales", 8, 7.5),
    ("SALES-ELIF", "Sales - Elif", "branch", "Sales", 6, 13.1),
]
GB = 1_000_000_000


def wait_for_server(c: httpx.Client) -> None:
    for _ in range(120):
        try:
            if c.get("/health").status_code == 200:
                return
        except httpx.HTTPError:
            pass
        time.sleep(2)
    raise SystemExit("demo: server did not come up")


def admin_client() -> httpx.Client:
    c = httpx.Client(base_url=BASE, timeout=30)
    wait_for_server(c)
    c.post("/api/v1/auth/login", json={"username": "admin", "password": PASSWORD}).raise_for_status()
    return c


def success_event(h: dict, host: str, i: int, size_gb: float) -> None:
    httpx.post(BASE + "/api/v1/agent/jobs/events", headers=h, timeout=30, json={
        "attempt_id": str(uuid.uuid4()), "job_id": str(uuid.uuid4()), "outcome": "SUCCEEDED",
        "snapshot_id": uuid.uuid4().hex * 2, "slot_key": f"{host}-{i}-{uuid.uuid4().hex[:8]}", "consistency": "VSS",
        "bytes_added": random.randint(40, 400) * 1_000_000, "files_new": random.randint(5, 220),
        "duration_ms": random.randint(90, 900) * 1000, "logical_source_bytes": int(size_gb * GB),
        "restic_data_added": random.randint(30, 300) * 1_000_000,
    }).raise_for_status()


def seed(adm: httpx.Client) -> list[dict]:
    adm.patch("/api/v1/admin/sites/hq", json={"measured_upload_mbps": 100, "business_backup_budget_percent": 40, "max_concurrent_wan_backups": 2}).raise_for_status()
    adm.patch("/api/v1/admin/sites/branch", json={"measured_upload_mbps": 50, "business_backup_budget_percent": 40, "max_concurrent_wan_backups": 1}).raise_for_status()
    pcs = []
    for host, name, site, dept, past, size_gb in PCS:
        tok = adm.post("/api/v1/admin/enrollment-tokens", json={"label": host, "site_id": site, "department": dept}).json()["token"]
        d = httpx.post(BASE + "/api/v1/agent/enrollments", timeout=30, json={"token": tok, "hostname": host, "agent_version": "0.1.0"}).json()
        h = {"Authorization": f"Bearer {d['control_credential']}"}
        adm.patch(f"/api/v1/admin/devices/{d['device_id']}", json={"display_name": name})
        for i in range(past):
            success_event(h, host, i, size_gb)
        pcs.append({"host": host, "h": h, "size_gb": size_gb, "n": past, "seq": 0})
    httpx.post(BASE + "/api/v1/agent/user-messages", headers=pcs[6]["h"], timeout=30,
               json={"message": "I deleted last week's price list by mistake - can I get it back?"})
    return pcs


def heartbeat(pc: dict) -> None:
    pc["seq"] += 1
    try:
        httpx.post(BASE + "/api/v1/agent/heartbeat", headers=pc["h"], timeout=30,
                   json={"sequence": pc["seq"], "agent_version": "0.1.0"})
    except httpx.HTTPError:
        pass


def run_forever(pcs: list[dict]) -> None:
    running: dict[str, dict] = {}  # host -> {lease, pct}
    last_beat = 0.0
    while True:
        now = time.time()
        if now - last_beat > 60:
            for pc in pcs:
                heartbeat(pc)
            last_beat = now
        # start a backup on an idle computer; the server decides whether the
        # site's upload budget has room (SITE_CAPACITY_WAIT otherwise)
        idle = [pc for pc in pcs if pc["host"] not in running]
        if idle and len(running) < 3:
            pc = random.choice(idle)
            try:
                r = httpx.post(BASE + "/api/v1/agent/wan/leases/acquire", headers=pc["h"], timeout=30, json={
                    "attempt_id": str(uuid.uuid4()), "class": "NORMAL_BACKUP", "job_id": str(uuid.uuid4())}).json()
                if r.get("status") == "GRANTED":
                    running[pc["host"]] = {"pc": pc, "lease": r["lease_id"], "pct": 0.0}
            except httpx.HTTPError:
                pass
        for host, job in list(running.items()):
            pc = job["pc"]
            job["pct"] = min(100.0, job["pct"] + random.uniform(1.0, 3.0))
            total = int(pc["size_gb"] * GB)
            try:
                if job["pct"] >= 100:
                    httpx.post(f"{BASE}/api/v1/agent/wan/leases/{job['lease']}/release", headers=pc["h"], timeout=30, json={"reason": "completed"})
                    pc["n"] += 1
                    success_event(pc["h"], host, pc["n"], pc["size_gb"])
                    del running[host]
                    continue
                pct = round(job["pct"], 1)
                httpx.post(f"{BASE}/api/v1/agent/wan/leases/{job['lease']}/renew", headers=pc["h"], timeout=30, json={
                    "percent_done": pct, "files_done": int(4200 * pct / 100), "total_files": 4200,
                    "bytes_done": int(total * pct / 100), "total_bytes": total})
            except httpx.HTTPError:
                running.pop(host, None)
        time.sleep(10)


def main() -> None:
    adm = admin_client()
    if STATE.exists():
        pcs = json.loads(STATE.read_text())
    else:
        pcs = seed(adm)
        STATE.parent.mkdir(parents=True, exist_ok=True)
        STATE.write_text(json.dumps(pcs))
    print(f"demo: {len(pcs)} computers ready at http://localhost:8080 (admin / {PASSWORD})", flush=True)
    run_forever(pcs)


if __name__ == "__main__":
    main()
