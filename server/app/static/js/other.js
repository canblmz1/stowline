"use strict";
/* Giriş, Yedekler, Geri Yükleme, Olaylar, Ayarlar ve "Yeni bilgisayar ekle". */

/* ---------- giriş ---------- */
function renderLogin() {
  const root = document.getElementById("root");
  root.innerHTML = `<div class="min-h-screen flex items-center justify-center p-4 bg-gradient-to-br from-primary via-primary-container to-secondary">
    <form id="login-form" class="w-full max-w-sm bg-surface-container-lowest rounded-2xl shadow-2xl border border-outline-variant/40 p-8">
      <div class="flex items-center gap-3 mb-6"><div class="w-11 h-11 rounded-xl bg-primary text-on-primary flex items-center justify-center shadow-sm">${icon("security", "text-[26px] fill")}</div>
        <div class="leading-tight"><div class="font-headline-md text-headline-md font-bold text-primary">Stowline</div><div class="font-label-sm text-label-sm text-outline font-semibold">${esc(orgName())}</div></div></div>
      <h1 class="font-headline-md text-headline-md font-bold text-on-surface">Yönetici girişi</h1>
      <p class="font-body-md text-body-md text-outline mt-1 mb-5">Pilot yerel hesabı (SSO değildir).</p>
      <label class="${UI.label}">Kullanıcı adı</label><input id="lg-user" class="${UI.input}" value="admin" autocomplete="username"/>
      <label class="${UI.label} mt-4">Şifre</label>
      <div class="relative"><input id="lg-pass" type="password" class="${UI.input} pr-10" autocomplete="current-password"/><button type="button" id="lg-eye" class="absolute right-2 top-1.5 text-outline hover:text-on-surface" aria-label="Göster/gizle">${icon("visibility", "text-[20px]")}</button></div>
      <p id="lg-err" class="text-error font-body-md text-body-md mt-3 min-h-[20px]"></p>
      <button type="submit" class="${UI.btnPrimary} w-full mt-2">Giriş yap</button>
      <p class="mt-6 text-center font-code-sm text-code-sm text-outline">Stowline · sürüm ${esc(App.state.version || "")}</p>
    </form></div>`;
  $("#lg-eye").onclick = () => {
    const i = $("#lg-pass");
    i.type = i.type === "password" ? "text" : "password";
  };
  $("#login-form").onsubmit = async (e) => {
    e.preventDefault();
    try {
      await api("/api/v1/auth/login", { method: "POST", body: JSON.stringify({ username: $("#lg-user").value, password: $("#lg-pass").value }), noAuthRedirect: true });
      App.state.me = await api("/api/v1/admin/me");
      App.shellReady = false;
      if (!App.timer) App.timer = setInterval(() => App.liveTick(), 10000);
      location.hash = "#/genel-bakis";
      App.render();
    } catch (err) {
      $("#lg-err").textContent = "Giriş başarısız";
      $("#lg-pass").select();
    }
  };
}

const hostnameOf = (id) => {
  const d = App.state.devices.find((x) => x.id === id);
  return d ? deviceName(d) : String(id || "").slice(0, 8);
};

/* ---------- Yedekler (tümü) ---------- */
App.ui.backupFilter = { q: "", outcome: "" };
Pages["yedekler"] = {
  live: false,
  async render(view) {
    const F = App.ui.backupFilter;
    const items = (await api("/api/v1/admin/backups")).items || [];
    const rows = items.filter((a) => (!F.outcome || a.outcome === F.outcome) && (!F.q || `${deviceName(a)} ${a.hostname || ""} ${a.snapshot_id || ""}`.toLowerCase().includes(F.q.toLowerCase())));
    view.innerHTML = `
      <section class="flex flex-col sm:flex-row sm:items-center justify-between gap-4 ${UI.card} p-5">
        <div><h1 class="font-headline-lg text-headline-lg text-primary font-bold tracking-tight">Yedekler</h1><p class="font-body-md text-body-md text-outline mt-0.5">Kısmi ve iptal edilen denemeler tazelik olarak sayılmaz.</p></div>
        <div class="flex items-center gap-3"><div class="relative w-64">${icon("search", "absolute left-3 top-2.5 text-[18px] text-outline")}<input id="b-q" class="${UI.input} pl-9" placeholder="Bilgisayar veya snapshot ara" value="${esc(F.q)}" autocomplete="off"/></div>
          <select id="b-out" class="${UI.inputBase} w-44"><option value="">Tüm sonuçlar</option>${Object.entries(OUTCOME).filter(([k]) => k !== "RUNNING").map(([k, v]) => `<option value="${k}" ${F.outcome === k ? "selected" : ""}>${v.label}</option>`).join("")}</select></div>
      </section>
      <section class="${UI.card} overflow-x-auto">${
        rows.length
          ? `<table class="w-full text-left border-collapse"><thead><tr class="bg-surface-container-low border-b border-outline-variant"><th class="${UI.th}">Bilgisayar</th><th class="${UI.th}">Sonuç</th><th class="${UI.th}">Snapshot</th><th class="${UI.th}">Hata</th><th class="${UI.th}">VSS / Tutarlılık</th><th class="${UI.th} text-right">Bitiş</th></tr></thead><tbody class="divide-y divide-outline-variant/60">${rows
              .map(
                (a) => `<tr class="hover:bg-surface-container-low/70"><td class="${UI.td}">${a.device_id ? `<a href="#/device/${esc(a.device_id)}" class="font-code-md text-code-md font-semibold text-on-surface hover:underline">${esc(deviceName(a) || hostnameOf(a.device_id))}</a>` : "—"}</td><td class="${UI.td}">${outcomePill(a.outcome)}</td><td class="${UI.td} font-code-sm text-code-sm">${esc(a.snapshot_id ? a.snapshot_id.slice(0, 12) : "—")}</td><td class="${UI.td}" title="${esc(a.error_class)}">${esc(attemptError(a) || "—")}</td><td class="${UI.td}">${esc(a.consistency || "—")}</td><td class="${UI.td} font-code-sm text-code-sm text-right">${esc(fmt(a.ended_at))}</td></tr>`
              )
              .join("")}</tbody></table>`
          : `<div class="p-12 text-center text-outline">Kayıt bulunamadı.</div>`
      }</section>`;
    $("#b-q").oninput = (e) => {
      F.q = e.target.value;
      Pages["yedekler"].render(view).then(() => {
        const i = $("#b-q");
        i.focus();
        i.setSelectionRange(i.value.length, i.value.length);
      });
    };
    $("#b-out").onchange = (e) => ((F.outcome = e.target.value), Pages["yedekler"].render(view));
  },
};

/* ---------- Geri Yükleme (tümü) ---------- */
Pages["geri-yukleme"] = {
  live: false,
  async render(view) {
    const devices = App.state.devices.filter((d) => d.lifecycle === "ACTIVE");
    const reqs = (await api("/api/v1/admin/restore-requests")).items || [];
    view.innerHTML = `
      <section class="${UI.card} p-5"><h1 class="font-headline-lg text-headline-lg text-primary font-bold tracking-tight">Geri Yükleme</h1><p class="font-body-md text-body-md text-outline mt-0.5">Dosyalar yalnızca onaylı bekleme (staging) alanına yazılır; orijinaller asla ezilmez.</p></section>
      <div class="rounded-lg border border-amber-300 bg-amber-50 text-amber-900 px-4 py-3 font-body-md text-body-md">Geri yüklenen dosyaları çalıştırmayın. Bilgisayar yedekleme sürerken geri yükleme isteğini işleyemez.</div>
      <section class="grid grid-cols-1 lg:grid-cols-12 gap-6">
        <form id="gr-form" class="lg:col-span-5 ${UI.card} p-5">
          <label class="${UI.label}">Bilgisayar</label><select id="gr-dev" class="${UI.input}">${devices.map((d) => `<option value="${esc(d.id)}">${esc(deviceName(d))}</option>`).join("")}</select>
          <label class="${UI.label} mt-4">Snapshot</label><select id="gr-snap" class="${UI.input} font-code-md text-code-md"><option value="">Yükleniyor…</option></select>
          <label class="${UI.label} mt-4">Yollar (satır başına bir yol; snapshot içi)</label><textarea id="gr-paths" rows="4" class="${UI.textarea} font-code-md text-code-md" placeholder="/C/Users/…"></textarea>
          <button type="submit" class="${UI.btnPrimary} mt-4">${icon("play_arrow", "text-[18px]")}<span>Bekleme alanına geri yükle</span></button>
        </form>
        <div class="lg:col-span-7 ${UI.card} p-5"><h3 class="font-headline-sm text-headline-sm font-bold text-on-surface mb-3">İstekler</h3>${
          reqs.length
            ? `<div class="overflow-x-auto"><table class="w-full text-left border-collapse"><thead><tr class="bg-surface-container-low border-y border-outline-variant"><th class="${UI.th}">Bilgisayar</th><th class="${UI.th}">Snapshot</th><th class="${UI.th}">Durum</th><th class="${UI.th}">Mod</th></tr></thead><tbody class="divide-y divide-outline-variant/60">${reqs.map((r) => `<tr><td class="${UI.td} font-code-md text-code-md">${esc(deviceName(r) || hostnameOf(r.device_id))}</td><td class="${UI.td} font-code-sm text-code-sm">${esc(String(r.snapshot_id).slice(0, 12))}</td><td class="${UI.td}">${restorePill(r.state)}</td><td class="${UI.td}">${esc(RESTORE_MODE_TR[r.destination_mode] || r.destination_mode)}</td></tr>`).join("")}</tbody></table></div>`
            : `<div class="py-8 text-center text-outline">Henüz geri yükleme isteği yok.</div>`
        }</div></section>`;
    const loadSnaps = async () => {
      const id = $("#gr-dev").value;
      const sel = $("#gr-snap");
      if (!id) return (sel.innerHTML = `<option value="">Bilgisayar yok</option>`);
      try {
        const det = await api(`/api/v1/admin/devices/${id}`);
        const snaps = (det.attempts || []).filter((a) => a.snapshot_id);
        sel.innerHTML = snaps.length ? snaps.map((a) => `<option value="${esc(a.snapshot_id)}">${esc(a.snapshot_id.slice(0, 12))} · ${esc(fmt(a.ended_at))}</option>`).join("") : `<option value="">Tamamlanmış yedek yok</option>`;
      } catch (e) {
        sel.innerHTML = `<option value="">${esc(e.message)}</option>`;
      }
    };
    $("#gr-dev").onchange = loadSnaps;
    loadSnaps();
    $("#gr-form").onsubmit = async (e) => {
      e.preventDefault();
      const selections = $("#gr-paths").value.split(/\n/).map((s) => s.trim()).filter(Boolean);
      try {
        await post("/api/v1/admin/restore-requests", { device_id: $("#gr-dev").value, snapshot_id: $("#gr-snap").value, selections });
        toast("Geri yükleme isteği kuyruğa alındı.");
        App.render({ live: true });
      } catch (err) {
        toast(err.message, "bad");
      }
    };
  },
};

/* ---------- Olaylar ---------- */
/* a computer by name (also for archived/revoked ones), a branch by its label, anything else stays blank */
/* who did it: a computer's own events (e.g. its user's message to IT) name that computer */
const auditActorText = (e) => (e.actor_type === "device" && e.device_id ? `${deviceName(e) || hostnameOf(e.device_id)}${deviceWasRenamed(e) ? " (" + e.hostname + ")" : ""}` : actorLabel(e.actor_type));
const auditTargetText = (e) => (e.device_id ? deviceName(e) || hostnameOf(e.device_id) : e.resource_type === "site" ? siteLabel(e.resource_id) : "");
App.ui.eventFilter = { q: "", result: "" };
Pages["olaylar"] = {
  live: false,
  async render(view) {
    const F = App.ui.eventFilter;
    const items = (await api("/api/v1/admin/audit-events")).items || [];
    const rows = items.filter((e) => (!F.result || e.result === F.result) && (!F.q || `${auditLabel(e)} ${e.action} ${auditTargetText(e)} ${auditActorText(e)}`.toLowerCase().includes(F.q.toLowerCase())));
    view.innerHTML = `
      <section class="flex flex-col sm:flex-row sm:items-center justify-between gap-4 ${UI.card} p-5">
        <div><h1 class="font-headline-lg text-headline-lg text-primary font-bold tracking-tight">Olaylar</h1><p class="font-body-md text-body-md text-outline mt-0.5">Denetim günlüğü · son ${items.length} kayıt</p></div>
        <div class="flex items-center gap-3"><div class="relative w-64">${icon("search", "absolute left-3 top-2.5 text-[18px] text-outline")}<input id="e-q" class="${UI.input} pl-9" placeholder="İşlem veya bilgisayar ara" value="${esc(F.q)}" autocomplete="off"/></div>
          <select id="e-res" class="${UI.inputBase} w-40"><option value="">Tüm sonuçlar</option><option value="OK" ${F.result === "OK" ? "selected" : ""}>Başarılı</option><option value="DENIED" ${F.result === "DENIED" ? "selected" : ""}>Reddedildi</option></select>
          <button type="button" id="e-csv" class="${UI.btnSecondary}">${icon("download", "text-[18px]")}<span>CSV indir</span></button></div>
      </section>
      <section class="${UI.card} overflow-x-auto">${
        rows.length
          ? `<table class="w-full text-left border-collapse"><thead><tr class="bg-surface-container-low border-b border-outline-variant"><th class="${UI.th}">Zaman</th><th class="${UI.th}">Kim</th><th class="${UI.th}">İşlem</th><th class="${UI.th}">Hedef</th><th class="${UI.th}">Sonuç</th></tr></thead><tbody class="divide-y divide-outline-variant/60">${rows
              .map(
                (e) => `<tr class="hover:bg-surface-container-low/70"><td class="${UI.td} font-code-sm text-code-sm whitespace-nowrap">${esc(fmt(e.occurred_at))}</td><td class="${UI.td}">${esc(auditActorText(e))}</td><td class="${UI.td}">${esc(auditLabel(e))}</td><td class="${UI.td} font-code-sm text-code-sm">${e.device_id ? `<a class="hover:underline" href="#/device/${esc(e.device_id)}">${esc(auditTargetText(e))}</a>` : esc(auditTargetText(e) || "—")}</td><td class="${UI.td}">${pill(e.result === "OK" ? "ok" : "bad", e.result === "OK" ? "Başarılı" : e.result)}</td></tr>`
              )
              .join("")}</tbody></table>`
          : `<div class="p-12 text-center text-outline">Olay bulunamadı.</div>`
      }</section>`;
    $("#e-q").oninput = (e) => {
      F.q = e.target.value;
      Pages["olaylar"].render(view).then(() => {
        const i = $("#e-q");
        i.focus();
        i.setSelectionRange(i.value.length, i.value.length);
      });
    };
    $("#e-res").onchange = (e) => ((F.result = e.target.value), Pages["olaylar"].render(view));
    $("#e-csv").onclick = () => {
      const esc2 = (s) => `"${String(s ?? "").replace(/"/g, '""')}"`;
      const csv = ["zaman,kim,islem,hedef,sonuc", ...rows.map((e) => [fmt(e.occurred_at), auditActorText(e), auditLabel(e), auditTargetText(e), e.result].map(esc2).join(","))].join("\n");
      const a = document.createElement("a");
      a.href = URL.createObjectURL(new Blob(["﻿" + csv], { type: "text/csv;charset=utf-8" }));
      a.download = "stowline-olaylar.csv";
      a.click();
      URL.revokeObjectURL(a.href);
    };
  },
};

/* ---------- Ayarlar ---------- */
App.ui.settingsTab = "subeler";
const SETTINGS_TABS = [["subeler", "Şubeler"], ["kurulum", "Kurulum kodları"], ["depolama", "Depolama"], ["hesap", "Yönetici hesabı"], ["surum", "Sürüm"]];

const SETUP_CODE_STATE = { ACTIVE: ["ok", "Geçerli"], USED_UP: ["warn", "Hakkı bitti"], EXPIRED: ["warn", "Süresi doldu"], REVOKED: ["bad", "İptal edildi"] };
const SETUP_CODE_HOURS = [[24, "1 gün"], [72, "3 gün"], [168, "7 gün"], [720, "30 gün"]];

const siteOptions = (selected, withAny) =>
  (withAny ? `<option value="" ${selected ? "" : "selected"}>Tüm şubeler</option>` : "") +
  ((App.state.dashboard || {}).sites || []).map((s) => `<option value="${esc(s.id)}" ${selected === s.id ? "selected" : ""}>${esc(siteLabel(s.id))}</option>`).join("");

// Shown once, right after it is created: the server keeps only a hash.
const setupCodeShown = (r) => `<div class="p-3 rounded-lg bg-surface-container-low border border-outline-variant">
  <div class="flex items-center gap-2"><code class="flex-1 px-2 py-1.5 rounded bg-inverse-surface text-inverse-on-surface font-code-md text-code-md break-all select-all">${esc(r.code)}</code><button type="button" data-copy="${esc(r.code)}" class="${UI.btnSecondary}">${icon("content_copy", "text-[18px]")}<span>Kopyala</span></button></div>
  <p class="mt-2 font-body-sm text-body-sm text-outline">Kod yalnızca şimdi gösterilir. ${esc(fmt(r.expires_at))}'e kadar, en fazla ${r.max_uses} kurulumda geçerlidir.</p></div>`;

async function setupCodesTab() {
  const codes = (await api("/api/v1/admin/setup-codes")).codes || [];
  const rows = codes
    .map((c) => {
      const [kind, label] = SETUP_CODE_STATE[c.state] || ["warn", c.state];
      return `<tr><td class="${UI.td}" data-no-i18n>${esc(c.label || "—")}</td><td class="${UI.td}">${c.site_id ? esc(siteLabel(c.site_id)) : "Tüm şubeler"}</td><td class="${UI.td} font-code-sm text-code-sm whitespace-nowrap">${c.uses} / ${c.max_uses}</td><td class="${UI.td} font-code-sm text-code-sm whitespace-nowrap">${esc(fmt(c.expires_at))}</td><td class="${UI.td}">${pill(kind, label)}</td>
        <td class="${UI.td} text-right">${c.state === "ACTIVE" ? `<button type="button" data-revoke-code="${esc(c.id)}" class="${UI.btnDangerOutline} whitespace-nowrap">Kodu iptal et</button>` : ""}</td></tr>`;
    })
    .join("");
  return `<div class="grid grid-cols-1 gap-6">
    <div class="${UI.card} p-5 max-w-xl"><h3 class="font-headline-sm text-headline-sm font-bold text-on-surface mb-2 flex items-center gap-2">${icon("key", "text-primary")}Yeni kurulum kodu</h3>
      <p class="mb-4 font-body-sm text-body-sm text-outline">Bilgisayarları kuran kişiye yönetici şifresi yerine bu kodu verin. Kod yalnızca bilgisayar kaydetmeye ve kaydettiği bilgisayarın adını, departmanını ve klasörlerini ayarlamaya yarar; panele giriş yapamaz.</p>
      <form id="sc-form" class="space-y-4">
        <div><label class="${UI.label}">Etiket</label><input id="sc-label" class="${UI.input}" maxlength="128" placeholder="Örn. Muhasebe kurulumları"/></div>
        <div class="grid grid-cols-2 gap-4">
          <div><label class="${UI.label}">Şube</label><select id="sc-site" class="${UI.input}">${siteOptions("", true)}</select></div>
          <div><label class="${UI.label}">Geçerlilik</label><select id="sc-hours" class="${UI.input}">${SETUP_CODE_HOURS.map(([h, l]) => `<option value="${h}" ${h === 72 ? "selected" : ""}>${l}</option>`).join("")}</select></div>
        </div>
        <div><label class="${UI.label}">En fazla kaç bilgisayar</label><input id="sc-uses" type="number" min="1" max="500" value="10" class="${UI.input}"/></div>
        <div class="flex justify-end"><button type="submit" class="${UI.btnPrimary}">${icon("add", "text-[18px]")}<span>Kod oluştur</span></button></div>
      </form>
      <div id="sc-out" class="mt-4"></div></div>
    <div class="${UI.card} p-5"><h3 class="font-headline-sm text-headline-sm font-bold text-on-surface mb-3">Kodlar</h3>${
      codes.length
        ? `<div class="overflow-x-auto"><table class="w-full text-left border-collapse"><thead><tr class="bg-surface-container-low border-y border-outline-variant"><th class="${UI.th}">Etiket</th><th class="${UI.th}">Şube</th><th class="${UI.th}">Kullanım</th><th class="${UI.th}">Bitiş</th><th class="${UI.th}">Durum</th><th class="${UI.th}"></th></tr></thead><tbody class="divide-y divide-outline-variant/60">${rows}</tbody></table></div>`
        : `<div class="py-6 text-center text-outline">Henüz kurulum kodu yok.</div>`
    }</div></div>`;
}

function bindSetupCodes(view) {
  const form = $("#sc-form", view);
  if (form)
    form.onsubmit = async (e) => {
      e.preventDefault();
      try {
        const r = await post("/api/v1/admin/setup-codes", {
          label: $("#sc-label", view).value.trim(),
          site_id: $("#sc-site", view).value,
          hours: parseInt($("#sc-hours", view).value, 10),
          max_uses: parseInt($("#sc-uses", view).value, 10) || 1,
        });
        await Pages["ayarlar"].render(view);
        $("#sc-out", view).innerHTML = setupCodeShown(r);
        bindCopy(view);
      } catch (err) {
        toast(err.message, "bad");
      }
    };
  $$("[data-revoke-code]", view).forEach((b) => {
    b.onclick = async () => {
      if (!confirm("Bu kod iptal edilsin mi? Kodla devam eden kurulumlar da durur.")) return;
      try {
        await post(`/api/v1/admin/setup-codes/${b.getAttribute("data-revoke-code")}/revoke`);
        toast("Kurulum kodu iptal edildi.");
        Pages["ayarlar"].render(view);
      } catch (err) {
        toast(err.message, "bad");
      }
    };
  });
}
const GATEWAY_REG_TR = { configured: "Hazır", unconfigured: "Yapılandırılmamış", error: "Hata var" };
const STORAGE_KIND_TR = { REST_GATEWAY: "REST ağ geçidi", PILOT_RCLONE_DRIVE: "Google Drive (pilot)" };
const STORAGE_STATUS_TR = { IMPLEMENTED: "Hazır", PLANNED: "Planlandı" };
const NOT_TR = { production: "üretim", "1.0": "1.0" };

function siteSettingsCard(s) {
  const sch = s.schedule || {};
  const paused = !!s.pause_new_wan_admissions;
  return `<div class="${UI.card} p-5">
    <div class="flex items-center justify-between mb-4"><div class="flex items-center gap-2">${icon("domain", "text-primary")}<h3 class="font-headline-sm text-headline-sm font-bold text-on-surface">${esc(siteLabel(s.id))} Şubesi</h3></div>${pill(paused ? "warn" : "ok", paused ? "Duraklatıldı" : "Normal")}</div>
    <div class="grid grid-cols-2 gap-4">
      <div><label class="${UI.label}">Ölçülen yükleme (Mbps)</label><input data-f="mbps" type="number" min="1" step="0.1" class="${UI.input}" value="${s.measured_upload_mbps ?? ""}" placeholder="zorunlu"/></div>
      <div><label class="${UI.label}">Yedekleme bütçesi (%)</label><input data-f="pct" type="number" min="5" max="50" class="${UI.input}" value="${s.business_backup_budget_percent ?? 20}"/></div>
    </div>
    <dl class="mt-4 space-y-1.5 font-code-sm text-code-sm">
      <div class="flex justify-between"><dt class="text-outline">Hesaplanan yükleme sınırı</dt><dd class="text-on-surface">${s.compiled_agent_kibps != null ? fmtNum(s.compiled_agent_kibps) + " KiB/sn" : sch.limit_upload_kib ? fmtNum(sch.limit_upload_kib) + " KiB/sn" : "—"}</dd></div>
      <div class="flex justify-between"><dt class="text-outline">Pencere</dt><dd class="text-on-surface">Başlangıç ${esc(sch.eligibility_start || "—")} · en geç ${esc(sch.preferred_latest_start || "—")} · durdurma ${esc(sch.hard_stop || "—")}</dd></div>
      <div class="flex justify-between"><dt class="text-outline">Kuyruk</dt><dd class="text-on-surface">Aktif ${s.active ?? 0} · Sırada ${s.queued ?? 0}</dd></div>
      <div class="flex justify-between"><dt class="text-outline">Gateway trafiği</dt><dd class="text-on-surface">${s.ingress_mbps != null ? Number(s.ingress_mbps).toFixed(2).replace(".", DEC_SEP) + " Mbps" : "—"}</dd></div>
    </dl>
    <div class="mt-4 flex flex-wrap gap-2"><button type="button" data-save="${esc(s.id)}" class="${UI.btnPrimary}">${icon("save", "text-[18px]")}<span>Kaydet</span></button>
      <button type="button" data-toggle="${esc(s.id)}" data-paused="${paused ? "1" : ""}" class="${paused ? UI.btnSecondary : UI.btnDangerOutline}">${paused ? "Yeni yedeklemeleri sürdür" : "Yeni yedeklemeleri durdur"}</button></div>
    <p class="mt-3 font-body-sm text-body-sm text-outline">Durdurma yalnızca yeni WAN başlatmalarını engeller; veri silmez ve çalışan işi iptal etmez. Pencere saatleri şube kuralıdır ve sunucu ayarında tutulur.</p>
  </div>`;
}

function passwordModal() {
  const m = openModal({
    title: "Yönetici şifresini değiştir",
    html: `<form id="pw-form" class="space-y-4">
      <div><label class="${UI.label}">Mevcut şifre</label><input id="pw-cur" type="password" class="${UI.input}" autocomplete="current-password"/></div>
      <div><label class="${UI.label}">Yeni şifre (en az 12 karakter)</label><input id="pw-new" type="password" class="${UI.input}" autocomplete="new-password"/><div class="mt-2 h-1.5 rounded-full bg-surface-container-highest overflow-hidden"><div id="pw-bar" class="h-full w-0 bg-red-500 transition-all"></div></div></div>
      <div><label class="${UI.label}">Yeni şifre (tekrar)</label><input id="pw-rep" type="password" class="${UI.input}" autocomplete="new-password"/></div>
      <p id="pw-err" class="text-error font-body-md text-body-md min-h-[20px]"></p>
      <div class="flex justify-end gap-2"><button type="button" data-cancel class="${UI.btnSecondary}">Vazgeç</button><button type="submit" class="${UI.btnPrimary}">Şifreyi değiştir</button></div></form>`,
  });
  $("[data-cancel]", m.el).onclick = () => m.close();
  $("#pw-new", m.el).oninput = (e) => {
    const v = e.target.value;
    const score = Math.min(4, (v.length >= 12) + (v.length >= 16) + (/[A-Z]/.test(v) && /[a-z]/.test(v)) + /[0-9\W]/.test(v));
    const bar = $("#pw-bar", m.el);
    bar.style.width = `${score * 25}%`;
    bar.className = `h-full transition-all ${score <= 1 ? "bg-red-500" : score <= 2 ? "bg-amber-500" : "bg-emerald-500"}`;
  };
  $("#pw-form", m.el).onsubmit = async (e) => {
    e.preventDefault();
    if ($("#pw-new", m.el).value !== $("#pw-rep", m.el).value) return ($("#pw-err", m.el).textContent = "Yeni şifreler aynı değil.");
    try {
      await post("/api/v1/admin/me/password", { current_password: $("#pw-cur", m.el).value, new_password: $("#pw-new", m.el).value });
      m.close();
      toast("Şifre değiştirildi. Yeni şifreni parola yöneticine kaydet.");
    } catch (err) {
      $("#pw-err", m.el).textContent = err.message;
    }
  };
}

Pages["ayarlar"] = {
  live: false,
  async render(view) {
    const tab = App.ui.settingsTab;
    const dash = App.state.dashboard || {};
    let inner = "";
    if (tab === "kurulum") inner = await setupCodesTab();
    else if (tab === "subeler") inner = `<div class="grid grid-cols-1 xl:grid-cols-2 gap-6">${(dash.sites || []).map(siteSettingsCard).join("") || `<div class="${UI.card} p-8 text-outline">Şube yok.</div>`}</div>`;
    else if (tab === "depolama") {
      const profiles = (await api("/api/v1/admin/storage-profiles")).items || [];
      const usage = App.state.usage && App.state.usage.available ? App.state.usage : null;
      inner = `<div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
        <div class="${UI.card} p-5"><div class="flex items-center justify-between mb-3"><h3 class="font-headline-sm text-headline-sm font-bold text-on-surface flex items-center gap-2">${icon("cloud", "text-primary")}Depolama</h3>${pill(dash.wan_ready === false ? "bad" : "ok", dash.wan_ready === false ? "Hazır değil" : "Bağlı ✓")}</div>
          <dl class="font-body-md text-body-md"><div class="flex justify-between py-2 border-b border-outline-variant/60"><dt class="text-outline">Kayıt durumu</dt><dd>${esc(GATEWAY_REG_TR[dash.gateway_registration] || dash.gateway_registration || "—")}</dd></div><div class="flex justify-between py-2"><dt class="text-outline">Toplam şifreli veri</dt><dd class="font-code-md text-code-md">${usage ? esc(fmtBytes(usage.total_bytes)) : "—"}</dd></div></dl></div>
        <div class="${UI.card} p-5"><h3 class="font-headline-sm text-headline-sm font-bold text-on-surface mb-3">Depolama profilleri</h3>${
          profiles.length ? `<div class="overflow-x-auto"><table class="w-full text-left border-collapse"><thead><tr class="bg-surface-container-low border-y border-outline-variant"><th class="${UI.th}">Tür</th><th class="${UI.th}">Etiket</th><th class="${UI.th}">Durum</th></tr></thead><tbody class="divide-y divide-outline-variant/60">${profiles.map((p) => `<tr><td class="${UI.td}">${esc(STORAGE_KIND_TR[p.kind] || p.kind)}</td><td class="${UI.td} font-code-sm text-code-sm">${esc(p.label)}</td><td class="${UI.td}">${esc(STORAGE_STATUS_TR[p.status] || p.status)}</td></tr>`).join("")}</tbody></table></div>` : `<div class="py-6 text-center text-outline">Profil yok.</div>`
        }</div></div>`;
    } else if (tab === "hesap") {
      const me = App.state.me || {};
      inner = `<div class="${UI.card} p-5 max-w-xl"><h3 class="font-headline-sm text-headline-sm font-bold text-on-surface mb-4 flex items-center gap-2">${icon("manage_accounts", "text-primary")}Yönetici hesabı</h3>
        <dl class="font-body-md text-body-md"><div class="flex justify-between py-2 border-b border-outline-variant/60"><dt class="text-outline">Kullanıcı adı</dt><dd class="font-code-md text-code-md">${esc(me.username || "")}</dd></div><div class="flex justify-between py-2"><dt class="text-outline">Rol</dt><dd>${me.role === "ADMIN" ? "Yönetici" : esc(me.role || "")}</dd></div></dl>
        <p class="mt-3 font-body-sm text-body-sm text-outline">Şifre saklanırken tek yönlü özetlenir; panelde gösterilemez. Şifreyi kaybederseniz yalnızca yeni bir şifre belirlenebilir; kendi şifrenizi bir parola yöneticisinde saklayın.</p>
        <button type="button" id="btn-pw" class="${UI.btnPrimary} mt-4">${icon("password", "text-[18px]")}<span>Şifreyi değiştir</span></button></div>`;
    } else {
      const v = await api("/api/v1/version");
      inner = `<div class="${UI.card} p-5 max-w-xl"><h3 class="font-headline-sm text-headline-sm font-bold text-on-surface mb-4">Sürüm</h3><dl class="font-body-md text-body-md">
        <div class="flex justify-between py-2 border-b border-outline-variant/60"><dt class="text-outline">Kontrol düzlemi</dt><dd class="font-code-md text-code-md">${esc(v.version)}</dd></div>
        <div class="flex justify-between py-2 border-b border-outline-variant/60"><dt class="text-outline">Şema</dt><dd class="font-code-md text-code-md">${esc(v.schema_version)}</dd></div>
        <div class="py-2"><dt class="text-outline mb-1">Durum</dt><dd class="font-body-sm text-body-sm text-on-surface-variant">${esc((v.not || []).length ? `Pilot sürümü: ${(v.not || []).map((t) => NOT_TR[t] || t).join(" ve ")} sürümü değildir.` : "—")}</dd></div></dl></div>`;
    }
    view.innerHTML = `<section class="${UI.card} p-5"><h1 class="font-headline-lg text-headline-lg text-primary font-bold tracking-tight">Ayarlar</h1>
      <nav class="mt-4 -mb-px flex flex-wrap gap-1 border-b border-outline-variant">${SETTINGS_TABS.map(([k, l]) => `<button type="button" data-tab="${k}" class="px-4 py-2.5 font-label-md text-label-md border-b-2 transition-colors ${tab === k ? "border-secondary text-primary font-semibold" : "border-transparent text-outline hover:text-on-surface"}">${l}</button>`).join("")}</nav></section>
      <div id="settings-body">${inner}</div>`;
    $$("[data-tab]", view).forEach((b) => (b.onclick = () => ((App.ui.settingsTab = b.getAttribute("data-tab")), Pages["ayarlar"].render(view))));
    $$("[data-save]", view).forEach((b) => {
      b.onclick = async () => {
        const card = b.closest("div.p-5");
        try {
          await patch(`/api/v1/admin/sites/${b.getAttribute("data-save")}`, { measured_upload_mbps: parseFloat($("[data-f=mbps]", card).value), business_backup_budget_percent: parseInt($("[data-f=pct]", card).value, 10) });
          toast("Şube ölçümü kaydedildi.");
          App.render({ live: true });
        } catch (e) {
          toast(e.message, "bad");
        }
      };
    });
    $$("[data-toggle]", view).forEach((b) => {
      b.onclick = async () => {
        const paused = b.getAttribute("data-paused") === "1";
        try {
          await post(`/api/v1/admin/sites/${b.getAttribute("data-toggle")}/${paused ? "resume" : "pause"}`);
          toast(paused ? "Yeni yedeklemeler sürdürüldü." : "Yeni yedeklemeler durduruldu.");
          App.render({ live: true });
        } catch (e) {
          toast(e.message, "bad");
        }
      };
    });
    const pw = $("#btn-pw");
    if (pw) pw.onclick = passwordModal;
    if (tab === "kurulum") bindSetupCodes(view);
  },
};

/* ---------- Yeni bilgisayar ekle ---------- */
function openAddComputer() {
  const known = new Set(App.state.devices.map((d) => d.id));
  let site = (((App.state.dashboard || {}).sites || [])[0] || {}).id || "hq";
  let dept = "IT";
  let poll = null;
  const m = openModal({ title: "Yeni bilgisayar ekle", size: "max-w-lg", onClose: () => poll && clearInterval(poll), html: "" });
  const stepBar = (n) => `<div class="flex items-center gap-2 mb-5">${["Şube ve departman", "Kurulum", "Bağlantı"].map((l, i) => `<div class="flex items-center gap-2 ${i ? "flex-1" : ""}">${i ? `<div class="h-px flex-1 bg-outline-variant"></div>` : ""}<span class="w-6 h-6 rounded-full flex items-center justify-center font-label-md text-label-md ${i + 1 <= n ? "bg-primary text-on-primary" : "bg-surface-container-high text-outline"}">${i + 1}</span><span class="font-label-md text-label-md ${i + 1 === n ? "text-on-surface" : "text-outline"}">${l}</span></div>`).join("")}</div>`;
  const step1 = () => {
    m.body.innerHTML = `${stepBar(1)}<div class="space-y-4">
      <div><label class="${UI.label}">Şube</label><select id="ac-site" class="${UI.input}">${siteOptions(site, false)}</select></div>
      <div><label class="${UI.label}">Departman</label><select id="ac-dept" class="${UI.input}">${DEPARTMENTS.map((x) => `<option value="${x}" ${dept === x ? "selected" : ""}>${esc(DEPT_LABEL[x] || x)}</option>`).join("")}</select></div>
      <div class="flex justify-end"><button type="button" id="ac-next" class="${UI.btnPrimary}">Devam</button></div></div>`;
    $("#ac-next", m.el).onclick = () => {
      site = $("#ac-site", m.el).value;
      dept = $("#ac-dept", m.el).value;
      step2();
    };
  };
  const step2 = () => {
    m.body.innerHTML = `${stepBar(2)}<ol class="space-y-3 font-body-md text-body-md text-on-surface-variant">
      <li class="flex gap-2"><span class="font-bold text-primary">1.</span><span>Aşağıdan bir kurulum kodu oluşturun (7 gün, 10 bilgisayar için geçerli).</span></li>
      <li class="flex gap-2"><span class="font-bold text-primary">2.</span><span>Bilgisayarda şu dosyayı çalıştırın: <strong>Stowline Setup.exe</strong>. Sihirbazda kodu yazın; şube: <strong>${esc(siteLabel(site))}</strong>, departman: <strong>${esc(DEPT_LABEL[dept] || dept)}</strong>.</span></li>
      <li class="flex gap-2"><span class="font-bold text-primary">3.</span><span>“Yedeklemeyi Başlat”a basınca bilgisayar burada görünür.</span></li></ol>
      <div class="mt-4"><button type="button" id="ac-setup-code" class="${UI.btnPrimary}">${icon("key", "text-[18px]")}<span>Kurulum kodu oluştur</span></button><div id="ac-setup-code-out" class="mt-2"></div></div>
      <div class="mt-4 p-3 rounded-lg bg-surface-container-low border border-outline-variant"><div class="font-label-md text-label-md font-bold text-on-surface mb-1">Yedek yöntem: kayıt kodu</div><p class="font-body-sm text-body-sm text-outline mb-2">Sihirbaz kullanılamıyorsa tek kullanımlık kod üretin (15 dakika geçerli).</p><button type="button" id="ac-token" class="${UI.btnSecondary}">Kayıt kodu oluştur</button><div id="ac-token-out" class="mt-2"></div></div>
      <div class="mt-5 flex justify-between"><button type="button" id="ac-back" class="${UI.btnSecondary}">Geri</button><button type="button" id="ac-next2" class="${UI.btnPrimary}">Bağlantıyı bekle</button></div>`;
    $("#ac-back", m.el).onclick = step1;
    $("#ac-next2", m.el).onclick = step3;
    $("#ac-setup-code", m.el).onclick = async () => {
      try {
        const r = await post("/api/v1/admin/setup-codes", { label: "Yeni bilgisayar", site_id: site, hours: 168, max_uses: 10 });
        $("#ac-setup-code-out", m.el).innerHTML = setupCodeShown(r);
        bindCopy(m.el);
      } catch (e) {
        toast(e.message, "bad");
      }
    };
    $("#ac-token", m.el).onclick = async () => {
      try {
        const r = await post("/api/v1/admin/enrollment-tokens", { label: "panel", minutes: 15, site_id: site, department: dept });
        $("#ac-token-out", m.el).innerHTML = `<div class="flex items-center gap-2"><code class="flex-1 px-2 py-1.5 rounded bg-inverse-surface text-inverse-on-surface font-code-sm text-code-sm break-all select-all">${esc(r.token)}</code><button type="button" data-copy="${esc(r.token)}" class="${UI.btnSecondary}">Kopyala</button></div><p class="mt-1 font-body-sm text-body-sm text-outline">Kod bir kez gösterilir ve ${esc(fmtTime(r.expires_at))}'e kadar geçerlidir.</p>`;
        bindCopy(m.el);
      } catch (e) {
        toast(e.message, "bad");
      }
    };
  };
  const step3 = () => {
    m.body.innerHTML = `${stepBar(3)}<div id="ac-live" class="p-6 rounded-lg bg-surface-container-low border border-dashed border-outline-variant text-center"><div class="flex items-center justify-center gap-2 text-secondary font-label-md text-label-md">${pill("live", "Bilgisayar bekleniyor", true)}</div><p class="mt-2 font-body-sm text-body-sm text-outline">Kurulum tamamlanınca bu pencere kendiliğinden güncellenir.</p></div>
      <div class="mt-5 flex justify-between"><button type="button" id="ac-back3" class="${UI.btnSecondary}">Geri</button><button type="button" id="ac-done" class="${UI.btnPrimary}">Kapat</button></div>`;
    $("#ac-back3", m.el).onclick = () => (clearInterval(poll), step2());
    $("#ac-done", m.el).onclick = () => m.close();
    poll = setInterval(async () => {
      try {
        const list = (await api("/api/v1/admin/devices")).items || [];
        const fresh = list.find((d) => !known.has(d.id));
        if (fresh) {
          clearInterval(poll);
          $("#ac-live", m.el).innerHTML = `<div class="text-emerald-700 font-headline-sm text-headline-sm font-bold flex items-center justify-center gap-2">${icon("check_circle", "text-[24px] fill")}Bilgisayar bağlandı ✓</div><p class="mt-1 font-code-md text-code-md text-on-surface">${esc(fresh.hostname)}</p><a href="#/device/${esc(fresh.id)}" class="${UI.btnPrimary} mt-3">Bilgisayara git</a>`;
          rerender();
        }
      } catch {
        /* keep waiting */
      }
    }, 4000);
  };
  step1();
}
