"use strict";
/* Stowline admin panel: shared helpers, data layer, layout and router.
   Pages register themselves in `Pages` (dashboard.js, device.js, vault.js, other.js). */

const TZ = "Europe/Istanbul";
const Pages = {};

/* ---------- design-system class strings (kept whole so Tailwind can see them) ---------- */
const UI = {
  card: "bg-surface-container-lowest rounded-xl border border-outline-variant shadow-sm",
  inputBase:
    "h-9 px-3 text-body-md font-body-md bg-surface-container-low border border-outline-variant rounded-lg focus:outline-none focus:border-secondary focus:ring-2 focus:ring-secondary/20 transition-all placeholder:text-outline text-on-surface disabled:opacity-60",
  input:
    "w-full h-9 px-3 text-body-md font-body-md bg-surface-container-low border border-outline-variant rounded-lg focus:outline-none focus:border-secondary focus:ring-2 focus:ring-secondary/20 transition-all placeholder:text-outline text-on-surface disabled:opacity-60",
  textarea:
    "w-full px-3 py-2 text-body-md font-body-md bg-surface-container-low border border-outline-variant rounded-lg focus:outline-none focus:border-secondary focus:ring-2 focus:ring-secondary/20 transition-all placeholder:text-outline text-on-surface",
  label: "block font-label-md text-label-md text-on-surface-variant mb-1",
  btnPrimary:
    "inline-flex items-center justify-center gap-2 bg-primary hover:bg-primary-container text-on-primary px-4 py-2 rounded-lg font-label-md text-label-md font-medium shadow-sm active:scale-[0.98] transition-all disabled:opacity-50 disabled:cursor-not-allowed disabled:active:scale-100",
  btnAccent:
    "inline-flex items-center justify-center gap-2 bg-secondary-container hover:bg-secondary text-on-secondary px-4 py-2 rounded-lg font-label-md text-label-md font-medium shadow-sm active:scale-[0.98] transition-all disabled:opacity-50 disabled:cursor-not-allowed disabled:active:scale-100",
  btnSecondary:
    "inline-flex items-center justify-center gap-1.5 px-3 py-1.5 rounded-lg border border-outline-variant bg-surface-container-lowest hover:bg-surface-container text-on-surface font-label-md text-label-md font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed",
  btnDanger:
    "inline-flex items-center justify-center gap-1.5 px-3 py-1.5 rounded-lg bg-error hover:bg-red-800 text-on-error font-label-md text-label-md font-medium transition-colors disabled:opacity-50",
  btnDangerOutline:
    "inline-flex items-center justify-center gap-1.5 px-3 py-1.5 rounded-lg border border-red-300 bg-surface-container-lowest hover:bg-red-50 text-error font-label-md text-label-md font-medium transition-colors disabled:opacity-50",
  th: "py-2.5 px-4 font-label-sm text-label-sm text-outline uppercase tracking-wider",
  td: "py-3 px-4",
};

const TONES = {
  ok: { pill: "bg-emerald-50 text-emerald-700 border-emerald-200", dot: "bg-emerald-500", text: "text-emerald-700" },
  warn: { pill: "bg-amber-50 text-amber-800 border-amber-200", dot: "bg-amber-500", text: "text-amber-800" },
  bad: { pill: "bg-red-50 text-red-700 border-red-200", dot: "bg-red-500", text: "text-red-700" },
  live: { pill: "bg-blue-50 text-blue-700 border-blue-200", dot: "bg-blue-600", text: "text-blue-700" },
  neutral: { pill: "bg-surface-container text-on-surface-variant border-outline-variant", dot: "bg-outline", text: "text-on-surface-variant" },
};

/* ---------- small helpers ---------- */
const esc = (s) =>
  String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const icon = (name, cls = "") => `<span class="material-symbols-outlined ${cls}">${name}</span>`;
const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function parseIso(s) {
  if (!s) return null;
  const t = /([zZ]|[+-]\d\d:?\d\d)$/.test(s) ? s : s + "Z";
  const d = new Date(t);
  return isNaN(d) ? null : d;
}
/* dates and numbers follow the UI language (see i18n.js) */
const UI_LOCALE = window.stowlineLang === "en" ? "en-GB" : "tr-TR";
const DEC_SEP = UI_LOCALE === "tr-TR" ? "," : ".";
const dtf = new Intl.DateTimeFormat(UI_LOCALE, { timeZone: TZ, day: "2-digit", month: "2-digit", year: "numeric", hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
const tmf = new Intl.DateTimeFormat(UI_LOCALE, { timeZone: TZ, hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
const fmt = (iso) => {
  const d = parseIso(iso);
  return d ? dtf.format(d).replace(",", "") : "—";
};
const fmtTime = (iso) => {
  const d = parseIso(iso);
  return d ? tmf.format(d) : "—";
};
function ago(iso) {
  const d = parseIso(iso);
  if (!d) return "—";
  const s = Math.max(0, (Date.now() - d.getTime()) / 1000);
  if (s < 60) return "az önce";
  const m = s / 60;
  if (m < 60) return `${Math.floor(m)} dk önce`;
  const h = m / 60;
  if (h < 48) return `${Math.floor(h)} saat önce`;
  return `${Math.floor(h / 24)} gün önce`;
}
function elapsed(iso) {
  const d = parseIso(iso);
  if (!d) return "—";
  const m = Math.max(0, Math.floor((Date.now() - d.getTime()) / 60000));
  if (m < 1) return "1 dakikadan az";
  if (m < 60) return `${m} dk`;
  return `${Math.floor(m / 60)} sa ${m % 60} dk`;
}
function fmtBytes(n) {
  if (n == null || isNaN(n)) return "—";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let v = Number(n);
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024;
    i++;
  }
  return (i >= 2 ? v.toFixed(1) : v.toFixed(0)).replace(".", DEC_SEP) + " " + u[i];
}
const fmtNum = (n) => (n == null ? "—" : Number(n).toLocaleString(UI_LOCALE));
const deviceName = (d) => String((d && d.display_name) || (d && d.hostname) || "").trim();
const deviceWasRenamed = (d) => !!(d && String(d.display_name || "").trim() && d.display_name !== d.hostname);
function progressPct(d) {
  const raw = d && d.current_progress ? d.current_progress.percent_done : null;
  if (raw == null || !Number.isFinite(Number(raw))) return null;
  return Math.max(0, Math.min(100, Number(raw)));
}
const progressLabel = (d) => {
  if (d && d.current_progress && d.current_progress.phase === "FINALIZING") return "Yükleniyor";
  const p = progressPct(d);
  return p == null ? "Aktif" : `%${Math.round(p)}`;
};
const progressWidth = (d) => {
  const p = progressPct(d);
  return p == null ? "100%" : `${p}%`;
};

/* ---------- backup progress: bytes text, ETA, old-agent fallback ----------
   A single API response only ever carries the latest sample; estimating a
   transfer rate needs a short recent history. The browser keeps that
   history for the run's lifetime (module-level, not App.state, since
   App.refreshData() replaces state.devices wholesale every tick) -- the
   server holds no such history and doesn't need to. */
const PROGRESS_SAMPLES = new Map(); // device id -> [{t, bytesDone}], oldest first, capped
const PROGRESS_SAMPLE_CAP = 8;
const PROGRESS_SAMPLE_MIN_COUNT = 3;
const PROGRESS_SAMPLE_MIN_SPAN_MS = 15000;
const PROGRESS_OLD_AGENT_GRACE_MS = 60000;
const PROGRESS_ETA_EMA_ALPHA = 0.3;

function hasRealProgressTotal(d) {
  return !!(d && d.current_progress && d.current_progress.total_bytes);
}

function recordProgressSamples(devices) {
  const seenIds = new Set();
  for (const d of devices || []) {
    if (!d.backing_up || !hasRealProgressTotal(d)) continue;
    seenIds.add(d.id);
    const bytesDone = Number(d.current_progress.bytes_done || 0);
    const list = PROGRESS_SAMPLES.get(d.id) || [];
    const last = list[list.length - 1];
    if (!last || last.bytesDone !== bytesDone) {
      list.push({ t: Date.now(), bytesDone });
      if (list.length > PROGRESS_SAMPLE_CAP) list.shift();
      PROGRESS_SAMPLES.set(d.id, list);
    }
  }
  for (const id of [...PROGRESS_SAMPLES.keys()]) {
    if (!seenIds.has(id)) PROGRESS_SAMPLES.delete(id);
  }
}

function progressBytesText(d) {
  if (!hasRealProgressTotal(d)) return "";
  const p = d.current_progress;
  return `${fmtBytes(p.bytes_done || 0)} / ${fmtBytes(p.total_bytes)}`;
}

function etaDurationLabel(seconds) {
  const m = Math.round(seconds / 60);
  if (m < 1) return "1 dakikadan az";
  if (m < 60) return `${m} dakika`;
  const h = Math.floor(m / 60);
  const remM = m % 60;
  return remM ? `${h} saat ${remM} dakika` : `${h} saat`;
}

function progressEtaText(id, d) {
  // Takes `id` explicitly rather than reading `d.id`: the device-detail
  // page's `status` object (this function's other caller) carries
  // current_progress but has no `id` field of its own -- only the merged
  // device view (`dv.id`) does. Decoupling avoids a silent lookup miss there.
  if (!hasRealProgressTotal(d)) return "";
  // restic's percent counts bytes read, not uploaded: at 100% it may still be
  // uploading for minutes under a WAN limit. No invented ETA for that part.
  if (d.current_progress.phase === "FINALIZING") return "Dosyalar hazırlandı, buluta yükleme tamamlanıyor…";
  const samples = PROGRESS_SAMPLES.get(id) || [];
  if (samples.length < PROGRESS_SAMPLE_MIN_COUNT) return "Süre hesaplanıyor";
  const span = samples[samples.length - 1].t - samples[0].t;
  if (span < PROGRESS_SAMPLE_MIN_SPAN_MS) return "Süre hesaplanıyor";
  let rate = null;
  for (let i = 1; i < samples.length; i++) {
    const dtSeconds = (samples[i].t - samples[i - 1].t) / 1000;
    if (dtSeconds <= 0) continue;
    const instant = (samples[i].bytesDone - samples[i - 1].bytesDone) / dtSeconds;
    rate = rate == null ? instant : PROGRESS_ETA_EMA_ALPHA * instant + (1 - PROGRESS_ETA_EMA_ALPHA) * rate;
  }
  if (!rate || rate <= 0) return "Süre hesaplanıyor";
  const p = d.current_progress;
  const remaining = Math.max(0, Number(p.total_bytes) - Number(p.bytes_done || 0));
  return `Yaklaşık ${etaDurationLabel(remaining / rate)} kaldı`;
}

function progressOldAgentMessage(d) {
  if (!d || !d.backing_up || hasRealProgressTotal(d)) return "";
  const started = parseIso(d.backup_started_at);
  if (!started) return "";
  return Date.now() - started.getTime() > PROGRESS_OLD_AGENT_GRACE_MS ? "Bu ajan sürümü yüzde bilgisi göndermiyor." : "";
}

/* ---------- labels ---------- */
const HEALTH = {
  HEALTHY: { tone: "ok", label: "Sağlıklı" },
  LATE: { tone: "warn", label: "Geç" },
  MISSED: { tone: "bad", label: "Kaçırıldı" },
  FAILED: { tone: "bad", label: "Başarısız" },
  PARTIAL: { tone: "warn", label: "Kısmi" },
  OFFLINE: { tone: "bad", label: "Çevrimdışı" },
  NEVER_BACKED_UP: { tone: "neutral", label: "Hiç yedeklenmedi" },
  RESTORE_REQUIRED_ATTENTION: { tone: "warn", label: "Dikkat gerekiyor" },
};
const OUTCOME = {
  SUCCEEDED: { tone: "ok", label: "Başarılı" },
  PARTIAL: { tone: "warn", label: "Kısmi" },
  CANCELLED: { tone: "neutral", label: "İptal edildi" },
  FAILED: { tone: "bad", label: "Başarısız" },
  RUNNING: { tone: "live", label: "Yedekleniyor" },
};
const GATEWAY_TR = { unconfigured: "yapılandırılmamış", error: "hata var", configured: "hazır" };
const orgName = () => App.state.orgName || "Stowline";
function footerSites() {
  const sites = (App.state.dashboard || {}).sites || [];
  if (!sites.length) return "";
  const tz = ((sites[0] || {}).schedule || {}).timezone || "";
  return `<span>Şubeler: ${esc(sites.map((x) => x.display_name || x.id).join(" & "))}</span>${tz ? `<span>·</span><span>Saat dilimi: ${esc(tz)}</span>` : ""}`;
}
/* site names come from the server's sites configuration (STOWLINE_SITES_FILE) */
const siteLabel = (s) => {
  const id = String(s || "").toLowerCase();
  const site = ((App.state.dashboard || {}).sites || []).find((x) => String(x.id || "").toLowerCase() === id);
  return (site && site.display_name) || (s ? String(s).charAt(0).toUpperCase() + String(s).slice(1) : "Şube yok");
};
const DEPT_LABEL = { Satis: "Satış", Yonetim: "Yönetim", Diger: "Diğer", IK: "İK", Yedekparca: "Yedekparça" };
// "Finance" is the dealership's warranty department (the installer offers
// it first), not an old "no department" placeholder -- it used to be shown
// as "Atanmamış", which hid real computers under the wrong heading.
const DEPARTMENTS = ["Finance", "IT", "Finance", "HR", "Sales", "Service", "Parts", "Management", "Other"];
function deptOf(d) {
  const raw = String((d && d.department) || "").trim();
  if (!raw) return "Atanmamış";
  return DEPT_LABEL[raw] || raw;
}
const AUDIT_TR = {
  enroll: "Bilgisayar kaydedildi",
  enrollment_abort: "Kayıt iptal edildi",
  command_create: "Komut oluşturuldu",
  device_revoke: "Bilgisayar iptal edildi (revoke)",
  device_archive: "Bilgisayar arşivlendi",
  device_unarchive: "Bilgisayar arşivden çıkarıldı",
  device_patch: "Bilgisayar ayarı değişti",
  device_rename: "Bilgisayar adı değişti",
  secret_store: "Anahtar kaydedildi",
  secret_reveal: "Anahtar görüntülendi",
  selection_update: "Korunan dosyalar güncellendi",
  site_update: "Şube ayarı değişti",
  password_change: "Yönetici şifresi değişti",
  login: "Yönetici girişi",
  enrollment_token_create: "Kayıt kodu oluşturuldu",
  setup_policy_bind: "Kurulum politikası atandı",
  selection_save: "Korunan dosyalar kaydedildi",
  restore_request: "Geri yükleme istendi",
  policy_create: "Politika oluşturuldu",
  policy_assign: "Politika atandı",
  wan_pause: "Yeni yedeklemeler durduruldu",
  wan_resume: "Yedeklemeler sürdürüldü",
  site_patch: "Şube ayarı değişti",
  emergency_restore_profile: "Acil geri yükleme profili kaydedildi",
  self_service_restore: "Kullanıcı kendi geri yükledi",
  user_message: "Kullanıcı mesajı",
};
const ACTOR_TR = { operator: "Yönetici", device: "Bilgisayar" };
const actorLabel = (t) => ACTOR_TR[t] || t || "—";
const RESTORE_STATE = { QUEUED: { tone: "neutral", label: "Sırada" }, READY: { tone: "ok", label: "Hazır" }, FAILED: { tone: "bad", label: "Başarısız" } };
const restorePill = (s) => {
  const x = RESTORE_STATE[String(s || "").toUpperCase()] || { tone: "neutral", label: s || "—" };
  return pill(x.tone, x.label);
};
const RESTORE_MODE_TR = { STAGING: "Bekleme alanı" };
/* the outcome pill already says "iptal edildi"; only add words it does not carry */
const attemptError = (a) => (a.outcome === "CANCELLED" ? "" : a.error_title || a.error_class || "");
const COMMAND_TR = { RUN_BACKUP: "Şimdi yedekle", RUN_CANARY: "Canary", CANCEL_CURRENT_SAFE_OPERATION: "İptal", REFRESH_POLICY: "Politika yenile", REPORT_INVENTORY: "Envanter", APPLY_SELECTION: "Seçimi uygula", BROWSE_LOCAL_DIR: "Klasör gezinme", BROWSE_SNAPSHOT: "Yedek gezinme", RESTORE_TO_STAGING: "Geri yükleme" };

function statusOf(d) {
  if (d.lifecycle && d.lifecycle !== "ACTIVE") {
    return d.lifecycle === "ARCHIVED" ? { tone: "neutral", label: "Arşivlendi", key: "ARCHIVED" } : { tone: "bad", label: "İptal edildi", key: d.lifecycle };
  }
  if (d.backing_up) return { tone: "live", label: "Yedekleniyor", key: "RUNNING" };
  const h = HEALTH[d.health] || { tone: "neutral", label: d.health || "—" };
  return { ...h, key: d.health };
}
function pill(tone, text, live = false) {
  const t = TONES[tone] || TONES.neutral;
  const dot = live
    ? `<span class="relative flex h-2 w-2"><span class="animate-ping absolute inline-flex h-full w-full rounded-full ${t.dot} opacity-75"></span><span class="relative inline-flex rounded-full h-2 w-2 ${t.dot}"></span></span>`
    : `<span class="w-1.5 h-1.5 rounded-full ${t.dot}"></span>`;
  return `<span class="inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full border font-label-sm text-label-sm font-semibold whitespace-nowrap ${t.pill}">${dot}<span>${esc(text)}</span></span>`;
}
const statusPill = (d) => {
  const s = statusOf(d);
  return pill(s.tone, s.label, s.tone === "live");
};
const outcomePill = (o) => {
  const x = OUTCOME[String(o || "").toUpperCase()] || { tone: "neutral", label: o || "—" };
  return pill(x.tone, x.label, x.tone === "live");
};
function needsAttention(d) {
  if (d.lifecycle !== "ACTIVE" || d.backing_up) return false;
  const err = d.last_error_human || {};
  return ["FAILED", "PARTIAL", "LATE", "MISSED", "OFFLINE"].includes(d.health) || Boolean(err.code && err.code !== "");
}
function windowClosed(d) {
  const sp = d && d.site_policy;
  if (!sp || !sp.hard_stop || !sp.timezone) return null;
  const parts = new Intl.DateTimeFormat("en-GB", { timeZone: sp.timezone, hour: "2-digit", minute: "2-digit", hourCycle: "h23" }).format(new Date()).split(":");
  const [h, m] = sp.hard_stop.split(":").map(Number);
  return +parts[0] * 60 + +parts[1] >= h * 60 + m ? sp.hard_stop : null;
}
function windowText(d) {
  const sp = d && d.site_policy;
  return sp && sp.eligibility_start && sp.hard_stop ? `${sp.eligibility_start}–${sp.hard_stop}` : "—";
}

/* ---------- api ---------- */
class ApiError extends Error {
  constructor(message, status) {
    super(message);
    this.status = status;
  }
}
async function api(path, opts = {}) {
  const { noAuthRedirect, ...init } = opts;
  const res = await fetch(path, { credentials: "include", ...init, headers: { "Content-Type": "application/json", ...(init.headers || {}) } });
  if (res.status === 401 && !noAuthRedirect) {
    App.onUnauthenticated();
    throw new ApiError("Oturum açılmadı", 401);
  }
  const text = await res.text();
  let data = {};
  try {
    data = text ? JSON.parse(text) : {};
  } catch {
    data = { message: text };
  }
  if (!res.ok) {
    let msg = data.detail || data.message || `Hata ${res.status}`;
    if (Array.isArray(msg)) msg = msg.map((m) => m.msg || m).join("; ");
    throw new ApiError(String(msg), res.status);
  }
  return data;
}
const post = (path, body = {}) => api(path, { method: "POST", body: JSON.stringify(body) });
const put = (path, body = {}) => api(path, { method: "PUT", body: JSON.stringify(body) });
const patch = (path, body = {}) => api(path, { method: "PATCH", body: JSON.stringify(body) });

async function pollCommand(commandId, { timeoutMs = 90000, intervalMs = 1500 } = {}) {
  const start = Date.now();
  while (Date.now() - start < timeoutMs) {
    const r = await api(`/api/v1/admin/commands/${commandId}`);
    if (r.state === "SUCCEEDED") return { ok: true, result: r.result };
    if (["FAILED", "EXPIRED", "CANCELLED"].includes(r.state)) return { ok: false, state: r.state, result: r.result };
    await sleep(intervalMs);
  }
  return { ok: false, state: "TIMEOUT" };
}

/* ---------- toast + modal ---------- */
function toast(message, tone = "ok") {
  const box = document.getElementById("toasts");
  if (!box) return;
  const cls = tone === "bad" ? "bg-error text-on-error" : "bg-inverse-surface text-inverse-on-surface";
  const ico = tone === "bad" ? "error" : "check_circle";
  const el = document.createElement("div");
  el.className = `toast-in flex items-start gap-2 max-w-sm px-4 py-3 rounded-lg shadow-lg font-body-md text-body-md ${cls}`;
  el.innerHTML = `${icon(ico, "text-[20px] mt-0.5")}<span>${esc(message)}</span>`;
  box.appendChild(el);
  setTimeout(() => el.remove(), tone === "bad" ? 7000 : 4500);
}

function openModal({ title, html, size = "max-w-md", onClose }) {
  const root = document.getElementById("modal-root");
  const wrap = document.createElement("div");
  wrap.className = "fixed inset-0 z-[70] flex items-center justify-center p-4 bg-black/40";
  wrap.innerHTML = `<div class="bg-surface-container-lowest rounded-xl border border-outline-variant shadow-xl w-full ${size} max-h-[90vh] overflow-y-auto" role="dialog" aria-modal="true">
      <div class="flex items-center justify-between px-5 py-4 border-b border-outline-variant/60">
        <h3 class="font-headline-sm text-headline-sm font-bold text-on-surface">${esc(title)}</h3>
        <button type="button" data-close class="w-8 h-8 flex items-center justify-center rounded-lg text-outline hover:text-on-surface hover:bg-surface-container transition-colors" aria-label="Kapat">${icon("close")}</button>
      </div>
      <div class="p-5" data-body>${html}</div>
    </div>`;
  const onKey = (e) => {
    if (e.key === "Escape") close();
  };
  function close() {
    document.removeEventListener("keydown", onKey);
    wrap.remove();
    if (onClose) onClose();
  }
  wrap.addEventListener("mousedown", (e) => {
    if (e.target === wrap) close();
  });
  $("[data-close]", wrap).onclick = close;
  document.addEventListener("keydown", onKey);
  root.appendChild(wrap);
  const first = $("input, textarea, select", wrap);
  if (first) first.focus();
  return { el: wrap, body: $("[data-body]", wrap), close };
}

function confirmDialog({ title, message, confirmText = "Onayla", tone = "primary" }) {
  return new Promise((resolve) => {
    let settled = false;
    const done = (v) => {
      if (!settled) {
        settled = true;
        resolve(v);
      }
    };
    const btn = tone === "danger" ? UI.btnDanger : UI.btnPrimary;
    const m = openModal({
      title,
      onClose: () => done(false),
      html: `<p class="font-body-md text-body-md text-on-surface-variant leading-relaxed">${message}</p>
        <div class="mt-5 flex justify-end gap-2">
          <button type="button" data-no class="${UI.btnSecondary}">Vazgeç</button>
          <button type="button" data-yes class="${btn}">${esc(confirmText)}</button>
        </div>`,
    });
    $("[data-no]", m.el).onclick = () => m.close();
    $("[data-yes]", m.el).onclick = () => {
      done(true);
      m.close();
    };
  });
}

/* ---------- shared actions ---------- */
async function runBackup(device) {
  if (device.backing_up) {
    toast("Bu bilgisayarda yedekleme zaten sürüyor.");
    return;
  }
  const closed = windowClosed(device);
  if (closed) {
    toast(`Yedekleme penceresi kapalı: bugünkü sınır ${closed} geçti. Yarın ya da pencere sonunu uzatarak deneyin.`, "bad");
    return;
  }
  try {
    const queued = await post(`/api/v1/admin/devices/${device.id}/commands`, { kind: "RUN_BACKUP", payload: {} });
    toast(
      queued.deduplicated
        ? "Bu bilgisayar için yedekleme zaten sırada veya çalışıyor; ikinci bir kopya oluşturulmadı."
        : "Yedekleme komutu kuyruğa alındı. Bilgisayar yaklaşık 1 dakika içinde başlatır."
    );
  } catch (e) {
    toast(e.message, "bad");
  }
}

function pickDeviceAndRun() {
  const list = App.state.devices.filter((d) => d.lifecycle === "ACTIVE");
  if (!list.length) return toast("Yedeklenecek bilgisayar yok.", "bad");
  if (list.length === 1) return runBackup(list[0]);
  const m = openModal({
    title: "Hangi bilgisayar yedeklensin?",
    html: `<div class="space-y-1.5 max-h-72 overflow-y-auto">${list
      .map(
        (d) => `<button type="button" data-pick="${esc(d.id)}" class="w-full flex items-center justify-between gap-3 px-3 py-2.5 rounded-lg border border-outline-variant hover:bg-surface-container transition-colors text-left">
          <span class="font-code-md text-code-md font-semibold text-on-surface">${esc(deviceName(d))}</span>${statusPill(d)}</button>`
      )
      .join("")}</div>`,
  });
  $$("[data-pick]", m.el).forEach((b) => {
    b.onclick = () => {
      m.close();
      runBackup(list.find((d) => d.id === b.getAttribute("data-pick")));
    };
  });
}

/* ---------- popover menus (row actions) ---------- */
function closeMenus() {
  $$("[data-menu-open]").forEach((m) => m.remove());
}
document.addEventListener("click", (e) => {
  if (!e.target.closest("[data-menu-trigger]") && !e.target.closest("[data-menu-open]")) closeMenus();
});
function attachMenu(trigger, items) {
  trigger.setAttribute("data-menu-trigger", "1");
  trigger.onclick = (e) => {
    e.stopPropagation();
    const had = $("[data-menu-open]");
    closeMenus();
    if (had && had._owner === trigger) return;
    const r = trigger.getBoundingClientRect();
    const menu = document.createElement("div");
    menu._owner = trigger;
    menu.setAttribute("data-menu-open", "1");
    menu.className = "fixed z-[60] min-w-[190px] bg-surface-container-lowest border border-outline-variant rounded-lg shadow-lg py-1";
    menu.style.top = `${Math.min(r.bottom + 4, window.innerHeight - 20 - items.length * 36)}px`;
    menu.style.left = `${Math.max(8, r.right - 190)}px`;
    menu.innerHTML = items
      .map(
        (it, i) =>
          `<button type="button" data-i="${i}" ${it.disabled ? "disabled" : ""} class="w-full flex items-center gap-2 px-3 py-2 text-left font-label-md text-label-md ${it.danger ? "text-error hover:bg-red-50" : "text-on-surface hover:bg-surface-container"} disabled:opacity-40 disabled:cursor-not-allowed">${icon(it.icon || "chevron_right", "text-[18px]")}<span>${esc(it.label)}</span></button>`
      )
      .join("");
    document.body.appendChild(menu);
    $$("[data-i]", menu).forEach((b) => {
      b.onclick = (ev) => {
        ev.stopPropagation();
        closeMenus();
        items[+b.getAttribute("data-i")].run();
      };
    });
  };
}

/* ---------- shell: sidebar, top bar ---------- */
const NAV = [
  ["genel-bakis", "dashboard", "Genel Bakış"],
  ["bilgisayarlar", "devices", "Bilgisayarlar"],
  ["yedekler", "cloud_sync", "Yedekler"],
  ["geri-yukleme", "settings_backup_restore", "Geri Yükleme"],
  ["sifre-kasasi", "lock", "Şifre Kasası"],
  ["olaylar", "history", "Olaylar"],
  ["ayarlar", "settings", "Ayarlar"],
];
const PAGE_TITLE = Object.fromEntries(NAV.map(([k, , l]) => [k, l]));

function shellHtml() {
  return `<aside id="sidebar" class="fixed top-0 left-0 h-screen w-[260px] bg-surface-container-lowest border-r border-outline-variant flex flex-col justify-between py-4 px-3 z-40 shadow-sm select-none overflow-y-auto transition-transform duration-200 -translate-x-full lg:translate-x-0"></aside>
    <div id="sidebar-scrim" class="fixed inset-0 z-30 bg-black/40 hidden lg:hidden"></div>
    <div class="lg:ml-[260px] min-h-screen flex flex-col bg-background">
      <header id="topbar" class="sticky top-0 z-30 bg-surface-container-lowest border-b border-outline-variant h-16 px-4 lg:px-6 flex items-center justify-between gap-3 shadow-sm"></header>
      <main id="view" class="flex-1 p-4 lg:p-6 space-y-6 max-w-[1520px] mx-auto w-full min-w-0"></main>
      <footer id="footer" class="mt-auto px-4 lg:px-6 py-4 bg-surface-container-lowest border-t border-outline-variant flex flex-col sm:flex-row items-center justify-between text-body-sm font-body-sm text-outline"></footer>
    </div>`;
}

/* below lg the sidebar is a drawer: the state lives on the elements, so the 10 s re-render of its content keeps it */
function setNav(open) {
  const sb = $("#sidebar");
  const scrim = $("#sidebar-scrim");
  if (!sb || !scrim) return;
  sb.classList.toggle("-translate-x-full", !open);
  scrim.classList.toggle("hidden", !open);
}

function departmentTree() {
  const groups = new Map();
  for (const d of App.state.devices) {
    if (d.lifecycle !== "ACTIVE") continue;
    const k = deptOf(d);
    if (!groups.has(k)) groups.set(k, []);
    groups.get(k).push(d);
  }
  return [...groups.entries()].sort((a, b) => (a[0] === "Atanmamış") - (b[0] === "Atanmamış") || a[0].localeCompare(b[0], "tr"));
}

function renderSidebar() {
  const el = $("#sidebar");
  if (!el) return;
  const route = App.route;
  const groups = departmentTree();
  const openId = route.name === "device" ? route.params.id : null;
  const tree = groups.length
    ? groups
        .map(([dept, list]) => {
          const hasCurrent = list.some((d) => d.id === openId);
          const running = list.filter((d) => d.backing_up).length;
          const isOpen = hasCurrent || App.ui.openDepts.has(dept) || groups.length <= 3;
          const rows = isOpen
            ? `<div class="tree-branch ml-3 pl-2 py-0.5 space-y-0.5">${list
                .map((d) => {
                  const s = statusOf(d);
                  const cur = d.id === openId;
                  return `<a href="#/device/${esc(d.id)}" class="flex items-center justify-between gap-2 py-1 px-1.5 rounded text-[11px] font-code-sm ${cur ? "bg-surface-container text-primary font-semibold" : "text-on-surface hover:bg-surface-container"} transition-colors">
                      <span class="flex items-center gap-1.5 min-w-0"><span class="w-1.5 h-1.5 rounded-full shrink-0 ${TONES[s.tone].dot} ${s.tone === "live" ? "animate-ping" : ""}"></span><span class="truncate">${esc(deviceName(d))}</span></span>
                      ${s.tone === "live" ? `<span class="text-secondary text-[10px] shrink-0">Yedekleniyor</span>` : ""}
                    </a>`;
                })
                .join("")}</div>`
            : "";
          return `<div class="flex flex-col">
              <a href="#/bilgisayarlar?dept=${encodeURIComponent(dept)}" data-dept="${esc(dept)}" class="flex items-center justify-between py-1 px-1.5 rounded ${hasCurrent ? "bg-surface-container text-primary font-medium" : "text-on-surface-variant hover:bg-surface-container"} text-body-sm font-body-sm cursor-pointer transition-colors">
                <span class="flex items-center gap-1.5">${icon(isOpen ? "folder_open" : "folder", `text-[14px] ${hasCurrent ? "text-secondary" : "text-outline"}`)}<span>${esc(dept)}</span></span>
                <span class="font-code-sm text-code-sm ${running ? "bg-secondary text-on-secondary px-1 rounded-full text-[10px]" : "text-outline"}">${running ? `${running}/${list.length}` : list.length}</span>
              </a>${rows}</div>`;
        })
        .join("")
    : `<div class="px-1.5 py-2 text-body-sm font-body-sm text-outline">Henüz yedeklenen bilgisayar yok.</div>`;
  el.innerHTML = `<div class="flex flex-col gap-4">
      <div class="flex items-center gap-2.5 px-2">
        <div class="w-9 h-9 rounded-lg bg-primary flex items-center justify-center text-on-primary shadow-sm">${icon("security", "text-[22px] fill")}</div>
        <div class="flex flex-col leading-tight"><span class="font-headline-sm text-headline-sm font-bold text-primary tracking-tight">Stowline</span><span class="font-label-sm text-label-sm text-outline font-semibold">${esc(orgName())}</span></div>
      </div>
      <button type="button" id="cta-backup" class="w-full flex items-center justify-center gap-2 py-2 px-3 bg-secondary-container hover:bg-secondary text-on-secondary font-label-md text-label-md rounded-lg shadow-sm transition-colors duration-150 active:scale-[0.98]">${icon("cloud_sync", "text-[18px]")}<span>Şimdi Yedekle</span></button>
      <div class="mt-1 flex flex-col border border-outline-variant/60 rounded-lg p-2.5 bg-surface-container-low/60">
        <div class="flex items-center justify-between text-on-surface-variant font-label-sm text-label-sm uppercase tracking-wider mb-2 px-1">
          <span class="flex items-center gap-1 font-bold text-primary">${icon("corporate_fare", "text-[14px]")}<span>${esc(orgName())}</span></span>
          <span class="font-code-sm text-code-sm bg-surface-container px-1.5 py-0.5 rounded text-on-surface-variant">${groups.length} departman</span>
        </div>
        <div class="tree-branch ml-2 pl-2 space-y-1 text-on-surface-variant">${tree}</div>
      </div>
      <nav class="flex flex-col gap-1 mt-1">
        <span class="px-3 text-[10px] font-bold text-outline uppercase tracking-wider mb-1">Yönetim Menüsü</span>
        ${NAV.map(([key, ico, label]) => {
          const active = route.name === key || (key === "bilgisayarlar" && route.name === "device");
          return `<a href="#/${key}" class="flex items-center gap-3 px-3 py-2 rounded-lg font-label-md text-label-md transition-colors duration-150 ${
            active ? "bg-primary text-on-primary font-medium shadow-sm" : "text-on-surface-variant hover:bg-surface-container hover:text-on-surface"
          }">${icon(ico, `text-[20px] ${active ? "fill" : ""}`)}<span>${label}</span></a>`;
        }).join("")}
      </nav>
    </div>
    <div class="pt-3 border-t border-outline-variant/60 flex flex-col gap-1">
      <div class="flex items-center justify-between px-2 py-1.5 rounded text-body-sm font-body-sm text-on-surface-variant">
        <span class="flex items-center gap-2"><span class="w-2 h-2 rounded-full ${App.state.dashboard && App.state.dashboard.wan_ready === false ? "bg-red-500" : "bg-emerald-500"}"></span><span>Depolama bağlantısı</span></span>
        <span class="font-code-sm text-code-sm font-semibold ${App.state.dashboard && App.state.dashboard.wan_ready === false ? "text-red-700" : "text-emerald-700"}">${App.state.dashboard && App.state.dashboard.wan_ready === false ? "Hazır değil" : "Hazır"}</span>
      </div>
      <div class="px-2 pt-1 text-[10px] text-outline font-code-sm">v${esc(App.state.version || "")}</div>
    </div>`;
  $("#cta-backup").onclick = pickDeviceAndRun;
  el.onclick = (e) => {
    if (e.target.closest("a, #cta-backup")) setNav(false);
  };
}

function breadcrumbHtml() {
  const r = App.route;
  const root = `<a href="#/genel-bakis" class="hidden sm:inline text-outline hover:text-on-surface cursor-pointer">${esc(orgName())}</a>`;
  const sep = `<span class="hidden sm:inline text-outline-variant">/</span>`;
  if (r.name === "device") {
    const d = App.state.devices.find((x) => x.id === r.params.id);
    return `${root}${sep}<a href="#/bilgisayarlar" class="hidden sm:inline text-outline hover:text-on-surface">Bilgisayarlar</a>${d ? `${sep}<span class="hidden sm:inline text-outline">${esc(deptOf(d))}</span>` : ""}${sep}<span class="text-primary font-semibold font-code-md text-code-md truncate">${esc(d ? deviceName(d) : "…")}</span>`;
  }
  return `${root}${sep}<span class="text-primary font-semibold">${esc(PAGE_TITLE[r.name] || "")}</span>`;
}

function renderTopbar() {
  const el = $("#topbar");
  if (!el) return;
  const running = App.state.devices.filter((d) => d.backing_up).length;
  const attention = App.state.devices.filter(needsAttention).length;
  const me = App.state.me || {};
  const initials = (me.username || "A").slice(0, 2).toUpperCase();
  el.innerHTML = `<div class="flex items-center gap-3 lg:gap-6 min-w-0">
      <button type="button" id="btn-nav" class="lg:hidden -ml-1 w-9 h-9 shrink-0 flex items-center justify-center rounded-lg text-on-surface-variant hover:bg-surface-container" aria-label="Menüyü aç"><svg viewBox="0 0 24 24" class="w-6 h-6" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M4 6h16M4 12h16M4 18h16"/></svg></button>
      <div class="flex items-center gap-2 text-label-md font-label-md whitespace-nowrap min-w-0">${breadcrumbHtml()}</div>
      <form id="global-search" class="relative w-56 shrink hidden lg:block">
        ${icon("search", "absolute left-3 top-2.5 text-[18px] text-outline")}
        <input id="global-search-input" class="w-full h-9 pl-9 pr-3 text-body-sm font-body-sm bg-surface-container-low border border-outline-variant rounded-lg focus:outline-none focus:border-secondary focus:ring-2 focus:ring-secondary/20 transition-all placeholder:text-outline text-on-surface" placeholder="Bilgisayar ara…" type="text" autocomplete="off"/>
      </form>
    </div>
    <div class="flex items-center gap-2 sm:gap-4 shrink-0">
      ${
        running
          ? `<div class="flex items-center gap-2 px-3 py-1 bg-surface-container-high rounded-full border border-secondary/30 text-secondary text-label-sm font-label-sm font-medium whitespace-nowrap"><span class="relative flex h-2 w-2"><span class="animate-ping absolute inline-flex h-full w-full rounded-full bg-secondary opacity-75"></span><span class="relative inline-flex rounded-full h-2 w-2 bg-secondary"></span></span><span>${running}<span class="hidden sm:inline"> yedekleme sürüyor</span></span></div>`
          : `<div class="hidden sm:flex items-center gap-2 px-3 py-1 bg-surface-container rounded-full border border-outline-variant text-outline text-label-sm font-label-sm font-medium whitespace-nowrap"><span class="w-2 h-2 rounded-full bg-outline-variant"></span><span>Şu an yedekleme yok</span></div>`
      }
      <div class="flex items-center gap-1 sm:border-r border-outline-variant sm:pr-3">
        <span id="stowline-lang-slot" class="text-outline"></span>
        <a href="#/genel-bakis" class="w-8 h-8 flex items-center justify-center rounded-lg text-outline hover:text-on-surface hover:bg-surface-container relative transition-colors" title="${attention ? attention + " bilgisayar dikkat gerektiriyor" : "Bildirim yok"}">${icon("notifications", "text-[20px]")}${attention ? `<span class="absolute top-1.5 right-1.5 w-2 h-2 rounded-full bg-error"></span>` : ""}</a>
      </div>
      <div class="flex items-center gap-2.5 pl-1">
        <div class="w-8 h-8 rounded-full bg-primary-container text-on-secondary flex items-center justify-center font-bold text-xs">${esc(initials)}</div>
        <div class="hidden md:flex flex-col text-left"><span class="font-label-md text-label-md text-on-surface font-semibold">${esc(me.username || "")}</span><span class="text-[10px] text-outline leading-none">${me.role === "ADMIN" ? "Yönetici" : esc(me.role || "")}</span></div>
      </div>
      <button type="button" id="btn-logout" class="flex items-center gap-1 text-on-surface-variant hover:text-error text-label-md font-label-md px-2 py-1 rounded transition-colors ml-1">${icon("logout", "text-[18px]")}<span class="hidden sm:inline">Çıkış</span></button>
    </div>`;
  $("#btn-logout").onclick = App.logout;
  $("#btn-nav").onclick = () => setNav(true);
  $("#global-search").onsubmit = (e) => {
    e.preventDefault();
    const q = $("#global-search-input").value.trim();
    location.hash = q ? `#/bilgisayarlar?q=${encodeURIComponent(q)}` : "#/bilgisayarlar";
  };
}

function renderFooter() {
  const el = $("#footer");
  if (!el) return;
  el.innerHTML = `<div><strong>${esc(orgName())}</strong> · Stowline yönetim paneli</div>
    <div class="flex items-center gap-4 mt-2 sm:mt-0 font-code-sm text-code-sm">${footerSites()}</div>`;
}

/* ---------- the app: state, routing, refresh ---------- */
const App = {
  state: { me: null, dashboard: null, devices: [], usage: null, version: "", lastRefresh: null },
  route: { name: "genel-bakis", params: {}, query: {} },
  ui: { openDepts: new Set() },
  timer: null,
  shellReady: false,

  parseHash() {
    const raw = location.hash.replace(/^#\/?/, "");
    const [pathPart, queryPart = ""] = raw.split("?");
    const segs = pathPart.split("/").filter(Boolean);
    const query = Object.fromEntries(new URLSearchParams(queryPart));
    const name = segs[0] || "genel-bakis";
    if (name === "device") return { name, params: { id: segs[1] || "", tab: segs[2] || "genel" }, query };
    return { name, params: {}, query };
  },

  async start() {
    window.addEventListener("hashchange", () => {
      setNav(false);
      App.render();
    });
    document.addEventListener("keydown", (e) => {
      if (e.key === "Escape") setNav(false);
    });
    await api("/api/v1/version", { noAuthRedirect: true }).then((v) => ((this.state.version = v.version || ""), (this.state.orgName = v.org_name || ""))).catch(() => {});
    try {
      this.state.me = await api("/api/v1/admin/me", { noAuthRedirect: true });
    } catch {
      return this.onUnauthenticated();
    }
    await this.render();
    this.timer = setInterval(() => this.liveTick(), 10000);
  },

  onUnauthenticated() {
    this.state.me = null;
    this.shellReady = false;
    document.getElementById("root").innerHTML = "";
    if (typeof renderLogin === "function") renderLogin();
  },

  async logout() {
    try {
      await post("/api/v1/auth/logout");
    } catch {
      /* the session is gone either way */
    }
    App.onUnauthenticated();
  },

  async refreshData() {
    const usage = api("/api/v1/admin/usage", { noAuthRedirect: true }).catch(() => null);
    const [dashboard, devices] = await Promise.all([api("/api/v1/admin/dashboard"), api("/api/v1/admin/devices")]);
    this.state.dashboard = dashboard;
    this.state.devices = devices.items || [];
    recordProgressSamples(this.state.devices);
    this.state.usage = await Promise.race([usage, sleep(1500).then(() => this.state.usage)]);
    this.state.lastRefresh = new Date();
  },

  async render(opts = {}) {
    this.route = this.parseHash();
    const root = document.getElementById("root");
    if (!this.state.me) return;
    if (!this.shellReady) {
      root.innerHTML = shellHtml();
      $("#sidebar-scrim").onclick = () => setNav(false);
      this.shellReady = true;
    }
    try {
      await this.refreshData();
    } catch (e) {
      if (e.status === 401) return;
      toast(e.message, "bad");
    }
    renderSidebar();
    renderTopbar();
    renderFooter();
    const page = Pages[this.route.name] || Pages["genel-bakis"];
    const view = $("#view");
    if (!opts.live) window.scrollTo(0, 0);
    try {
      await page.render(view, this.route.params, this.route.query, { live: !!opts.live });
    } catch (e) {
      if (e.status === 401) return;
      view.innerHTML = `<div class="${UI.card} p-8 text-center"><div class="text-error font-semibold mb-1">Sayfa yüklenemedi</div><div class="text-outline">${esc(e.message)}</div></div>`;
    }
  },

  async liveTick() {
    if (document.hidden || !this.state.me || $("[data-modal-lock]")) return;
    const page = Pages[this.route.name];
    if (!page || !page.live || (page.liveWhen && !page.liveWhen(this.route))) return;
    if ($("#modal-root").children.length) return;
    await this.render({ live: true });
  },
};

const rerender = () => App.render({ live: true });
