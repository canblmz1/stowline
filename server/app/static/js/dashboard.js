"use strict";
/* Genel Bakış and Bilgisayarlar. */

function statCard({ label, ico, icoCls = "text-primary", value, unit = "", badge = "", foot = "", footCls = "text-outline", accent = false }) {
  return `<div class="${accent ? "border-secondary/30 bg-gradient-to-b from-surface-container-lowest to-surface-container-low" : "border-outline-variant bg-surface-container-lowest"} p-4 rounded-xl border shadow-sm flex flex-col justify-between">
    <div class="flex items-center justify-between ${accent ? "text-secondary" : "text-outline"}">
      <span class="font-label-md text-label-md font-medium">${esc(label)}</span>${icon(ico, `text-[20px] ${icoCls}`)}
    </div>
    <div class="mt-3 flex items-baseline gap-2">
      <span class="font-headline-xl text-headline-xl font-bold ${accent ? "text-secondary" : "text-on-surface"}">${value}</span>
      ${unit ? `<span class="font-headline-sm text-headline-sm font-semibold text-outline">${esc(unit)}</span>` : ""}${badge}
    </div>
    <div class="mt-2 text-[11px] font-medium ${footCls}">${foot}</div>
  </div>`;
}

/* the limit a computer is really held to: a running backup keeps the one it started with, otherwise the
   branch's compiled limit, and only then the schedule's fixed default */
function siteCompiledKib(siteId) {
  const s = ((App.state.dashboard || {}).sites || []).find((x) => x.id === siteId);
  return s ? s.compiled_agent_kibps : null;
}
function deviceLimitText(d) {
  const kib = (d.backing_up && d.run_limit_kibps) || siteCompiledKib(d.site_id) || (d.site_policy && d.site_policy.limit_upload_kib);
  return kib ? `${fmtNum(kib)} KiB/sn` : "—";
}
/* restic reads its limit once, so a run keeps the old one until the next run starts */
function pendingLimitNote(d) {
  const now = siteCompiledKib(d.site_id);
  return d.backing_up && d.run_limit_kibps && now && now !== d.run_limit_kibps
    ? `Şube sınırı şimdi ${fmtNum(now)} KiB/sn; bu yedekleme ${fmtNum(d.run_limit_kibps)} KiB/sn ile başladığı için yeni sınır bir sonraki çalıştırmada geçerli olur.`
    : "";
}
const pendingLimitHtml = (d) =>
  pendingLimitNote(d) ? `<div class="rounded-lg border border-amber-300 bg-amber-50 text-amber-900 px-3 py-2 font-body-sm text-body-sm">${esc(pendingLimitNote(d))}</div>` : "";

function usageBytesFor(d) {
  const u = App.state.usage;
  if (!u || !u.available || !u.devices) return null;
  const x = u.devices[d.id];
  return x && x.bytes != null ? x.bytes : null;
}

function liveBackupBlock(d) {
  const bytes = usageBytesFor(d);
  const pct = progressPct(d);
  return `<div class="p-4 rounded-lg bg-surface-container-low border border-outline-variant/80 flex flex-col gap-4">
    <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-2">
      <div class="flex items-center gap-3">
        <div class="w-10 h-10 rounded-lg bg-secondary/10 text-secondary flex items-center justify-center">${icon("laptop_windows", "text-[22px]")}</div>
        <div>
          <div class="flex items-center gap-2"><span class="font-code-md text-code-md font-bold text-on-surface">${esc(deviceName(d))}</span>${pill("live", "Yedekleniyor", true)}</div>
          <p class="font-body-sm text-body-sm text-outline mt-0.5">${esc(siteLabel(d.site_id))} · ${esc(deptOf(d))}</p>
        </div>
      </div>
      <a href="#/device/${esc(d.id)}" class="${UI.btnSecondary} self-start sm:self-center">${icon("visibility", "text-[16px]")}<span>Ayrıntı</span></a>
    </div>
    <div class="w-full">
      <div class="flex justify-between text-body-sm font-body-sm text-outline mb-1.5">
        <span class="flex items-center gap-1">${icon("cloud_upload", "text-[16px] text-secondary")}<span>Dosyalar taranıyor, şifreleniyor ve buluta aktarılıyor…</span></span>
        <span class="font-code-sm text-code-sm text-secondary font-semibold">${esc(progressLabel(d))}</span>
      </div>
      <div class="w-full h-3 bg-surface-container-highest rounded-full overflow-hidden p-0.5"><div class="h-full rounded-full bg-secondary ${pct == null ? "animate-progress-stripe" : "transition-[width] duration-500"}" style="width:${esc(progressWidth(d))}"></div></div>
      ${
        progressBytesText(d)
          ? `<div class="mt-1.5 flex items-center justify-between font-code-sm text-code-sm text-outline"><span>${esc(progressBytesText(d))}</span><span>${esc(progressEtaText(d.id, d))}</span></div>`
          : progressOldAgentMessage(d)
            ? `<p class="mt-1.5 font-body-sm text-body-sm text-outline">${esc(progressOldAgentMessage(d))}</p>`
            : ""
      }
    </div>
    <div class="flex flex-wrap items-center justify-between gap-2 text-body-sm font-body-sm pt-2 border-t border-outline-variant/60">
      <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-on-surface font-code-sm text-code-sm">
        <span><strong class="font-semibold text-primary">${esc(elapsed(d.backup_started_at))}</strong> geçti</span>
        <span class="text-outline">·</span>
        <span>Başladı <strong class="font-semibold text-primary">${esc(fmtTime(d.backup_started_at))}</strong></span>
        ${bytes != null ? `<span class="text-outline">·</span><span>Bulutta <strong class="font-semibold text-primary">${esc(fmtBytes(bytes))}</strong></span>` : ""}
        <span class="text-outline">·</span>
        <span>Yükleme sınırı <strong class="font-semibold text-primary">${esc(deviceLimitText(d))}</strong></span>
      </div>
      <div class="text-outline">Hedef: <span class="font-code-sm text-code-sm text-on-surface-variant font-medium">Bulut (şifreli)</span></div>
    </div>
    ${pendingLimitHtml(d)}
  </div>`;
}

function attentionCard(list) {
  if (!list.length) {
    return `<div class="${UI.card} p-5"><div class="flex items-start gap-3">
      <div class="w-9 h-9 rounded-lg bg-emerald-50 border border-emerald-200 text-emerald-700 flex items-center justify-center shrink-0">${icon("verified", "text-[20px]")}</div>
      <div><h3 class="font-headline-sm text-headline-sm font-bold text-on-surface">Dikkat gerektiren yok</h3><p class="font-body-sm text-body-sm text-outline mt-1">Şu an bir işlem gerektiren sorun görünmüyor.</p></div>
    </div></div>`;
  }
  return `<div class="rounded-xl border border-amber-300/80 p-5 shadow-sm bg-gradient-to-br from-surface-container-lowest to-amber-50/30"><div class="flex items-start gap-3">
    <div class="w-9 h-9 rounded-lg bg-amber-100 border border-amber-300 text-amber-700 flex items-center justify-center shrink-0">${icon("report_problem", "text-[20px]")}</div>
    <div class="flex-1 min-w-0">
      <div class="flex items-center justify-between"><h3 class="font-headline-sm text-headline-sm font-bold text-amber-900">Dikkat gerektirenler</h3><span class="px-2 py-0.5 bg-amber-100 text-amber-800 text-[10px] font-bold rounded-full font-code-sm">${list.length}</span></div>
      <div class="mt-2.5 space-y-2.5">${list
        .map((d) => {
          const err = d.last_error_human || {};
          const s = statusOf(d);
          return `<div class="p-3 bg-surface-container-lowest border border-amber-200 rounded-lg">
            <div class="font-label-md text-label-md font-bold text-on-surface flex items-center gap-1.5"><span class="w-2 h-2 rounded-full ${TONES[s.tone === "neutral" ? "warn" : s.tone].dot}"></span><span>${esc(err.title || s.label)}</span></div>
            <p class="font-body-sm text-body-sm text-on-surface-variant mt-1.5 leading-relaxed"><span class="font-code-sm text-code-sm font-semibold text-on-surface">${esc(deviceName(d))}</span> ${esc(err.summary || "Ayrıntı için bilgisayar sayfasına bakın.")}</p>
            <div class="mt-3 flex items-center justify-between pt-2 border-t border-outline-variant/40">
              <span class="text-[11px] text-outline font-code-sm">${esc(d.last_seen_at ? "Son sinyal " + ago(d.last_seen_at) : "")}</span>
              <a href="#/device/${esc(d.id)}" class="px-3 py-1 bg-amber-600 hover:bg-amber-700 text-on-secondary font-label-md text-label-md rounded-md transition-colors active:scale-[0.98]">İncele</a>
            </div></div>`;
        })
        .join("")}</div>
    </div></div></div>`;
}

function siteCard(s) {
  const sch = s.schedule || {};
  const paused = !!s.pause_new_wan_admissions;
  const busy = (s.active || 0) > 0;
  const tone = paused ? "warn" : s.status === "PROVIDER_UNAVAILABLE" ? "bad" : busy ? "live" : s.status === "WAIT" ? "warn" : "ok";
  const label = paused ? "Duraklatıldı" : s.status === "PROVIDER_UNAVAILABLE" ? "Sağlayıcı yok" : busy ? "Yedekleniyor" : s.status === "WAIT" ? "Ölçülmedi" : "Normal";
  const limit = s.compiled_agent_kibps || sch.limit_upload_kib;
  return `<div class="p-3.5 bg-surface-container-low rounded-lg border border-outline-variant flex flex-col gap-2.5">
    <div class="flex items-center justify-between">
      <div class="flex items-center gap-2">${icon("domain", "text-primary text-[18px]")}<span class="font-headline-sm text-headline-sm font-semibold text-on-surface">${esc(siteLabel(s.id))} Şubesi</span></div>
      ${pill(tone, label, tone === "live")}
    </div>
    <div class="space-y-1 font-code-sm text-code-sm">
      <div class="flex justify-between"><span class="text-outline">Yükleme sınırı:</span><span class="font-medium text-on-surface">${limit ? fmtNum(limit) + " KiB/sn" : "ölçülmedi"}</span></div>
      <div class="flex justify-between"><span class="text-outline">Yedekleme penceresi:</span><span class="font-medium text-on-surface">${sch.eligibility_start && sch.hard_stop ? esc(sch.eligibility_start + "–" + sch.hard_stop) : "—"}</span></div>
      <div class="flex justify-between"><span class="text-outline">İşlem kuyruğu:</span><span class="font-medium ${s.active ? "text-secondary" : "text-outline"}">Aktif ${s.active ?? 0} · Sırada ${s.queued ?? 0}</span></div>
    </div>
    <button type="button" data-site-toggle="${esc(s.id)}" data-paused="${paused ? "1" : ""}" class="w-full mt-1 py-1.5 px-3 bg-surface-container-lowest hover:bg-surface-container border border-outline-variant text-on-surface font-label-md text-label-md rounded-md transition-colors active:scale-[0.98]">${paused ? "Yeni yedeklemeleri sürdür" : "Yeni yedeklemeleri durdur"}</button>
  </div>`;
}

Pages["genel-bakis"] = {
  live: true,
  async render(view) {
    const S = App.state;
    const dash = S.dashboard || {};
    const devices = S.devices.filter((x) => x.lifecycle === "ACTIVE");
    const counts = dash.counts || {};
    const running = devices.filter((x) => x.backing_up);
    const attention = devices.filter(needsAttention);
    const total = dash.total_devices ?? devices.length;
    const healthy = counts.HEALTHY || 0;
    const usage = S.usage && S.usage.available ? S.usage : null;
    const sites = dash.sites || [];
    const nextRun = devices.map((d) => d.next_eligible_run).filter(Boolean).sort()[0];
    const siteCount = new Set(devices.map((d) => d.site_id).filter(Boolean)).size;
    const recent = dash.recent_attempts || [];
    const messages = dash.user_messages || [];

    const recentRows = [
      ...running.map(
        (d) => `<tr class="hover:bg-surface-container-low/70 transition-colors">
          <td class="${UI.td}"><a href="#/device/${esc(d.id)}" class="block"><div class="font-code-sm text-code-sm font-semibold text-on-surface">${esc(deviceName(d))}</div><div class="text-[11px] text-outline">${deviceWasRenamed(d) ? esc(d.hostname) + " · " : ""}${esc(siteLabel(d.site_id))} · ${esc(deptOf(d))}</div></a></td>
          <td class="${UI.td}">${pill("live", "Yedekleniyor", true)}</td>
          <td class="${UI.td} font-code-sm text-code-sm text-secondary font-medium">${esc(elapsed(d.backup_started_at))} (devam ediyor)</td>
          <td class="${UI.td} font-code-sm text-code-sm text-outline text-right">—</td></tr>`
      ),
      ...recent.slice(0, 6).map(
        (a) => `<tr class="hover:bg-surface-container-low/70 transition-colors">
          <td class="${UI.td}">${a.device_id ? `<a href="#/device/${esc(a.device_id)}" class="block">` : "<div>"}<div class="font-code-sm text-code-sm font-semibold text-on-surface">${esc(deviceName(a) || "—")}</div><div class="text-[11px] text-outline">${deviceWasRenamed(a) ? esc(a.hostname) + " · " : ""}${esc(siteLabel(a.site_id))} · ${esc(deptOf({ department: a.department }))}</div>${a.device_id ? "</a>" : "</div>"}</td>
          <td class="${UI.td}">${outcomePill(a.outcome)}</td>
          <td class="${UI.td} font-code-sm text-code-sm text-on-surface-variant">${a.duration_ms ? esc(elapsedMs(a.duration_ms)) : "—"}</td>
          <td class="${UI.td} font-code-sm text-code-sm text-on-surface text-right font-medium">${esc(fmt(a.ended_at))}</td></tr>`
      ),
    ].join("");

    view.innerHTML = `
      <section class="flex flex-col sm:flex-row sm:items-center justify-between gap-4 ${UI.card} p-5">
        <div><h1 class="font-headline-lg text-headline-lg text-primary font-bold tracking-tight">Genel Bakış</h1><p class="font-body-md text-body-md text-outline mt-0.5">Sistem ve yedekleme durumu canlı özeti</p></div>
        <div class="flex flex-wrap items-center gap-3">
          <div class="flex items-center gap-2 bg-surface-container-low border border-outline-variant px-3 py-1.5 rounded-lg text-body-sm font-body-sm text-on-surface-variant">
            ${icon("history", "text-[16px] text-outline")}<span>Son güncelleme: <strong class="font-code-sm text-code-sm text-on-surface">${esc(S.lastRefresh ? fmt(S.lastRefresh.toISOString()) : "—")}</strong></span>
            <button type="button" id="btn-refresh" class="ml-1 text-primary hover:text-secondary transition-all" title="Yenile">${icon("refresh", "text-[18px]")}</button>
          </div>
          <button type="button" id="btn-backup" class="${UI.btnPrimary} whitespace-nowrap">${icon("play_circle", "text-[18px]")}<span>Şimdi Yedekle</span></button>
        </div>
      </section>

      ${dash.wan_ready === false ? `<div class="rounded-lg border border-amber-300 bg-amber-50 text-amber-900 px-4 py-3 font-body-md text-body-md">Depolama bağlantısı hazır değil (${esc(GATEWAY_TR[dash.gateway_registration] || dash.gateway_registration || "yapılandırılmamış")}). Yeni yedeklemeler başlamaz.</div>` : ""}

      <section class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-5 gap-4">
        ${statCard({ label: "Yedeklenen bilgisayar", ico: "computer", value: total, unit: "aktif cihaz", foot: `${icon("check_circle", "text-[14px] text-emerald-600 align-middle")} ${siteCount || 0} şubede kayıtlı` })}
        ${statCard({
          label: "Sağlıklı",
          ico: "verified",
          icoCls: "text-emerald-600",
          value: `<span class="text-emerald-700">${healthy}</span>`,
          badge: total ? `<span class="px-2 py-0.5 rounded-full bg-emerald-50 text-emerald-700 font-label-sm text-label-sm border border-emerald-200">%${Math.round((healthy / total) * 100)}</span>` : "",
          foot: total && healthy === total ? "Hepsi sağlıklı" : total ? `${total - healthy} bilgisayar henüz sağlıklı değil` : "—",
          footCls: "text-emerald-700",
        })}
        ${statCard({
          label: "Dikkat gerektiren",
          ico: "warning",
          icoCls: "text-amber-500",
          value: `<span class="text-amber-600">${attention.length}</span>`,
          badge: attention.length ? `<span class="px-2 py-0.5 rounded-full bg-amber-50 text-amber-800 font-label-sm text-label-sm border border-amber-200">${esc(statusOf(attention[0]).label)}</span>` : "",
          foot: attention.length ? `${esc(deviceName(attention[0]))} kontrolü bekliyor` : "Sorun yok",
          footCls: "text-amber-800",
        })}
        ${statCard({
          label: "Şu an yedekleniyor",
          ico: "sync",
          icoCls: "text-secondary",
          value: running.length,
          accent: true,
          badge: running.length ? `<div class="flex items-center gap-1.5 px-2 py-0.5 rounded-full bg-surface-container text-secondary text-label-sm font-label-sm border border-secondary/20"><span class="w-1.5 h-1.5 rounded-full bg-secondary animate-ping"></span><span>Canlı</span></div>` : "",
          foot: running.length ? esc(running.map(deviceName).join(", ")) : "Şu an çalışan yedekleme yok",
          footCls: "text-secondary font-code-sm text-code-sm",
        })}
        ${
          usage
            ? (() => {
                const gb = usage.total_bytes / 1024 ** 3;
                const [v, u] = gb >= 1000 ? [(gb / 1024).toFixed(2).replace(".", DEC_SEP), "TB"] : [gb >= 10 ? Math.round(gb).toString() : gb.toFixed(1).replace(".", DEC_SEP), "GB"];
                return statCard({ label: "Buluttaki toplam veri", ico: "cloud", value: v, unit: u, foot: `<div class="flex justify-between"><span class="font-code-sm text-code-sm">Bulut · şifreli</span><span class="text-emerald-700 font-semibold font-code-sm">${esc(ago(usage.measured_at))}</span></div>` });
              })()
            : statCard({ label: "Buluttaki toplam veri", ico: "cloud", value: "—", foot: "Henüz ölçülmedi" })
        }
      </section>

      <section class="grid grid-cols-1 lg:grid-cols-12 gap-6">
        <div class="lg:col-span-8 space-y-6">
          <div class="${UI.card} p-5">
            <div class="flex items-center justify-between border-b border-outline-variant/60 pb-3">
              <div class="flex items-center gap-2">${icon("pending_actions", "text-secondary text-[22px]")}<h2 class="font-headline-sm text-headline-sm text-on-surface font-bold">Şu an yedekleme yapanlar</h2></div>
              <span class="font-code-sm text-code-sm text-outline">Canlı · 10 sn'de bir yenilenir</span>
            </div>
            <div class="mt-4 space-y-3">${
              running.length
                ? running.map(liveBackupBlock).join("")
                : `<div class="p-8 rounded-lg bg-surface-container-low border border-dashed border-outline-variant text-center flex flex-col items-center">
                    <div class="w-12 h-12 rounded-full bg-surface-container-high text-outline flex items-center justify-center mb-2">${icon("inbox", "text-[28px]")}</div>
                    <h4 class="font-headline-sm text-headline-sm font-bold text-on-surface">Şu an çalışan yedekleme yok</h4>
                    <p class="font-body-md text-body-md text-outline max-w-md mt-1">${nextRun ? `Sıradaki zamanlanmış çalışma: <strong class="text-on-surface">${esc(fmt(nextRun))}</strong>.` : "Zamanlanmış çalışma yok."} İstersen hemen başlatabilirsin.</p>
                  </div>`
            }</div>
          </div>
          <div class="${UI.card} p-5">
            <div class="flex items-center justify-between mb-4">
              <div class="flex items-center gap-2">${icon("table_chart", "text-primary text-[22px]")}<h2 class="font-headline-sm text-headline-sm text-on-surface font-bold">Son yedeklemeler</h2></div>
              <a class="text-secondary hover:underline font-label-md text-label-md flex items-center gap-1" href="#/yedekler"><span>Tümünü Gör</span>${icon("arrow_forward", "text-[16px]")}</a>
            </div>
            <div class="overflow-x-auto">${
              recentRows
                ? `<div class="overflow-x-auto"><table class="w-full text-left border-collapse"><thead><tr class="bg-surface-container-low border-y border-outline-variant"><th class="${UI.th}">Bilgisayar</th><th class="${UI.th}">Sonuç</th><th class="${UI.th}">Süre</th><th class="${UI.th} text-right">Bitiş Zamanı</th></tr></thead><tbody class="divide-y divide-outline-variant/60 font-body-md text-body-md">${recentRows}</tbody></table></div>`
                : `<div class="py-8 text-center text-outline">Henüz kayıtlı bir yedekleme denemesi yok.</div>`
            }</div>
          </div>
        </div>
        <div class="lg:col-span-4 space-y-6">
          ${attentionCard(attention)}
          <div class="${UI.card} p-5 space-y-4">
            <div class="flex items-center justify-between border-b border-outline-variant/60 pb-3">
              <div class="flex items-center gap-2">${icon("location_city", "text-primary text-[22px]")}<h3 class="font-headline-sm text-headline-sm text-on-surface font-bold">Şube Durumu</h3></div>
              <span class="font-code-sm text-code-sm text-outline">${sites.length} şube</span>
            </div>
            ${sites.map(siteCard).join("") || `<div class="text-outline">Şube yok.</div>`}
          </div>
        </div>
      </section>
      <section class="${UI.card} p-5">
        <div class="flex items-center justify-between border-b border-outline-variant/60 pb-3 mb-2">
          <div class="flex items-center gap-2">${icon("notifications", "text-primary text-[22px]")}<h3 class="font-headline-sm text-headline-sm text-on-surface font-bold">Kullanıcı mesajları</h3></div>
          <a href="#/olaylar" class="font-label-md text-label-md text-primary hover:underline">Tümü</a>
        </div>
        ${
          messages.length
            ? `<ul class="divide-y divide-outline-variant/60">${messages
                .map(
                  (m) => `<li class="py-2.5 flex items-start justify-between gap-4"><div class="min-w-0"><a href="#/device/${esc(m.device_id)}" class="font-body-md text-body-md font-semibold text-on-surface hover:underline">${esc(deviceName(m) || "Bilinmeyen bilgisayar")}</a><div class="text-[11px] text-outline">${deviceWasRenamed(m) ? esc(m.hostname) + " · " : ""}${esc(siteLabel(m.site_id))} · ${esc(deptOf(m))}</div><div class="font-body-md text-body-md text-on-surface-variant mt-1 break-words">“${esc(m.message)}”</div></div><div class="font-code-sm text-code-sm text-outline whitespace-nowrap">${esc(fmt(m.occurred_at))}</div></li>`
                )
                .join("")}</ul>`
            : `<div class="py-4 text-center text-outline">Henüz mesaj yok.</div>`
        }
      </section>`;

    $("#btn-refresh").onclick = () => rerender();
    $("#btn-backup").onclick = pickDeviceAndRun;
    $$("[data-site-toggle]", view).forEach((b) => {
      b.onclick = async () => {
        const id = b.getAttribute("data-site-toggle");
        const paused = b.getAttribute("data-paused") === "1";
        try {
          await post(`/api/v1/admin/sites/${id}/${paused ? "resume" : "pause"}`);
          toast(paused ? "Yeni yedeklemeler sürdürüldü." : "Yeni yedeklemeler durduruldu. Çalışan işler etkilenmez.");
          rerender();
        } catch (e) {
          toast(e.message, "bad");
        }
      };
    });
  },
};

function elapsedMs(ms) {
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s} sn`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m} dk ${s % 60} sn`;
  return `${Math.floor(m / 60)} sa ${m % 60} dk`;
}

/* ---------- Bilgisayarlar ---------- */
App.ui.filters = { q: "", site: "", dept: "", status: "" };
App.ui.showArchived = false;
App.ui.computersView = "list";

function computerMenuItems(d) {
  const items = [{ label: "Ayrıntı", icon: "visibility", run: () => (location.hash = `#/device/${d.id}`) }];
  if (d.lifecycle === "ACTIVE") {
    items.push({ label: "Şimdi yedekle", icon: "play_circle", run: () => runBackup(d) });
    items.push({
      label: "Arşivle",
      icon: "inventory_2",
      run: async () => {
        const ok = await confirmDialog({
          title: "Bu bilgisayar listeden çıkarılsın mı?",
          message: `<strong>${esc(deviceName(d))}</strong> arşivlenecek. Yedekleri silinmez, ancak bu bilgisayar sunucuya bağlanamaz. Arşivden geri alabilirsiniz.`,
          confirmText: "Arşivle",
        });
        if (!ok) return;
        try {
          await post(`/api/v1/admin/devices/${d.id}/archive`);
          toast(`${deviceName(d)} arşivlendi.`);
          rerender();
        } catch (e) {
          toast(e.message, "bad");
        }
      },
    });
  } else if (d.lifecycle === "ARCHIVED") {
    items.push({
      label: "Arşivden çıkar",
      icon: "unarchive",
      run: async () => {
        try {
          await post(`/api/v1/admin/devices/${d.id}/unarchive`);
          toast(`${deviceName(d)} arşivden çıkarıldı.`);
          rerender();
        } catch (e) {
          toast(e.message, "bad");
        }
      },
    });
  }
  return items;
}

function computerRow(d) {
  const s = statusOf(d);
  const pct = progressPct(d);
  const op = d.backing_up
    ? `<div class="min-w-[150px]"><div class="flex items-center justify-between gap-1.5 text-secondary font-label-md text-label-md font-semibold mb-1"><span>${esc(elapsed(d.backup_started_at))} geçti</span><span>${esc(progressLabel(d))}</span></div><div class="w-full h-1.5 bg-surface-container-highest rounded-full overflow-hidden"><div class="h-full rounded-full bg-secondary ${pct == null ? "animate-progress-stripe" : "transition-[width] duration-500"}" style="width:${esc(progressWidth(d))}"></div></div></div>`
    : `<span class="text-outline">${d.lifecycle === "ACTIVE" ? esc(d.next_eligible_run ? "Sıradaki: " + fmt(d.next_eligible_run) : "—") : "—"}</span>`;
  return `<tr class="hover:bg-surface-container-low/70 transition-colors cursor-pointer" data-href="#/device/${esc(d.id)}">
    <td class="${UI.td} w-8"><span class="block w-2.5 h-2.5 rounded-full ${TONES[s.tone].dot} ${s.tone === "live" ? "animate-pulse" : ""}"></span></td>
    <td class="${UI.td}"><div class="font-code-md text-code-md font-semibold text-on-surface">${esc(deviceName(d))}</div><div class="text-[11px] text-outline">${deviceWasRenamed(d) ? esc(d.hostname) + " · " : ""}Agent ${esc(d.agent_version || "—")}</div></td>
    <td class="${UI.td}">${esc(siteLabel(d.site_id))}</td>
    <td class="${UI.td}">${esc(deptOf(d))}</td>
    <td class="${UI.td}">${pill(s.tone, s.label, s.tone === "live")}</td>
    <td class="${UI.td}">${op}</td>
    <td class="${UI.td} font-code-sm text-code-sm">${esc(fmt(d.last_success_at))}</td>
    <td class="${UI.td} font-code-sm text-code-sm">${esc(fmtBytes(d.selected_source_bytes || null))}</td>
    <td class="${UI.td} text-right"><button type="button" data-row-menu="${esc(d.id)}" class="w-8 h-8 inline-flex items-center justify-center rounded-lg text-outline hover:text-on-surface hover:bg-surface-container transition-colors" aria-label="İşlemler">${icon("more_horiz")}</button></td>
  </tr>`;
}

function computerCard(d) {
  const s = statusOf(d);
  const pct = progressPct(d);
  return `<div class="${UI.card} p-4 flex flex-col gap-3 hover:border-primary/40 transition-colors">
    <div class="flex items-start justify-between gap-2">
      <a href="#/device/${esc(d.id)}" class="min-w-0"><div class="font-code-md text-code-md font-bold text-on-surface truncate">${esc(deviceName(d))}</div><div class="text-body-sm font-body-sm text-outline">${deviceWasRenamed(d) ? esc(d.hostname) + " · " : ""}${esc(siteLabel(d.site_id))} · ${esc(deptOf(d))}</div></a>
      ${pill(s.tone, s.label, s.tone === "live")}
    </div>
    ${d.backing_up ? `<div><div class="text-right text-label-sm font-label-sm font-semibold text-secondary mb-1">${esc(progressLabel(d))}</div><div class="w-full h-1.5 bg-surface-container-highest rounded-full overflow-hidden"><div class="h-full rounded-full bg-secondary ${pct == null ? "animate-progress-stripe" : "transition-[width] duration-500"}" style="width:${esc(progressWidth(d))}"></div></div></div>` : ""}
    <div class="grid grid-cols-2 gap-2 text-body-sm font-body-sm"><div><div class="text-outline">Son başarılı</div><div class="font-code-sm text-code-sm">${esc(fmt(d.last_success_at))}</div></div><div><div class="text-outline">Sıradaki</div><div class="font-code-sm text-code-sm">${esc(fmt(d.next_eligible_run))}</div></div></div>
    <button type="button" data-card-backup="${esc(d.id)}" class="${UI.btnPrimary} w-full" ${d.lifecycle !== "ACTIVE" ? "disabled" : ""}>${icon("play_circle", "text-[18px]")}<span>Şimdi yedekle</span></button>
  </div>`;
}

Pages["bilgisayarlar"] = {
  live: true,
  async render(view, params, query, ctx) {
    const F = App.ui.filters;
    if (!ctx.live) {
      F.q = query.q || "";
      F.dept = query.dept || "";
    }
    let list = App.state.devices.slice();
    if (App.ui.showArchived) {
      try {
        list = ((await api("/api/v1/admin/devices?scope=archived")).items || []).sort((a, b) => deviceName(a).localeCompare(deviceName(b), "tr"));
      } catch (e) {
        toast(e.message, "bad");
      }
    }
    const sites = [...new Set(list.map((d) => d.site_id).filter(Boolean))];
    const depts = [...new Set(list.map(deptOf))].sort((a, b) => a.localeCompare(b, "tr"));
    const matches = (d) => {
      const q = F.q.toLowerCase();
      if (q && !(`${deviceName(d)} ${d.hostname} ${deptOf(d)} ${d.id}`.toLowerCase().includes(q))) return false;
      if (F.site && d.site_id !== F.site) return false;
      if (F.dept && deptOf(d) !== F.dept) return false;
      if (F.status && statusOf(d).key !== F.status) return false;
      return true;
    };
    const rows = list.filter(matches);
    const runningCount = App.state.devices.filter((d) => d.backing_up).length;
    const statusOptions = [["", "Tüm durumlar"], ["RUNNING", "Yedekleniyor"], ...Object.entries(HEALTH).map(([k, v]) => [k, v.label])];
    view.innerHTML = `
      <section class="flex flex-col sm:flex-row sm:items-center justify-between gap-4 ${UI.card} p-5">
        <div><h1 class="font-headline-lg text-headline-lg text-primary font-bold tracking-tight">Bilgisayarlar</h1>
          <p class="font-body-md text-body-md text-outline mt-0.5">${App.ui.showArchived ? `${list.length} arşivlenmiş / iptal edilmiş kayıt` : `${runningCount ? runningCount + " bilgisayar yedekleniyor · " : ""}${App.state.devices.length} bilgisayar yedeklenen listesinde`}</p></div>
        <button type="button" id="btn-add" class="${UI.btnPrimary}">${icon("add", "text-[18px]")}<span>Yeni bilgisayar ekle</span></button>
      </section>
      <section class="${UI.card} p-4">
        <div class="flex flex-wrap items-center gap-3">
          <div class="relative w-72">${icon("search", "absolute left-3 top-2.5 text-[18px] text-outline")}<input id="f-q" class="${UI.input} pl-9" placeholder="Bilgisayar adı veya departman ara" value="${esc(F.q)}" autocomplete="off"/></div>
          <select id="f-site" class="${UI.inputBase} w-44"><option value="">Tüm şubeler</option>${sites.map((s) => `<option value="${esc(s)}" ${F.site === s ? "selected" : ""}>${esc(siteLabel(s))}</option>`).join("")}</select>
          <select id="f-dept" class="${UI.inputBase} w-48"><option value="">Tüm departmanlar</option>${depts.map((s) => `<option value="${esc(s)}" ${F.dept === s ? "selected" : ""}>${esc(s)}</option>`).join("")}</select>
          <select id="f-status" class="${UI.inputBase} w-48">${statusOptions.map(([k, l]) => `<option value="${k}" ${F.status === k ? "selected" : ""}>${esc(l)}</option>`).join("")}</select>
          <div class="ml-auto flex items-center gap-4">
            <label class="flex items-center gap-2 text-body-md font-body-md text-on-surface-variant cursor-pointer select-none"><input type="checkbox" id="f-archived" class="rounded border-outline-variant text-secondary focus:ring-secondary/30" ${App.ui.showArchived ? "checked" : ""}/><span>Arşivlenenleri göster</span></label>
            <div class="inline-flex p-1 bg-surface-container rounded-lg border border-outline-variant/60">
              <button type="button" data-view="list" class="px-2 py-1 rounded-md ${App.ui.computersView === "list" ? "bg-surface-container-lowest text-primary shadow-sm" : "text-outline"}" title="Liste">${icon("view_list", "text-[18px]")}</button>
              <button type="button" data-view="cards" class="px-2 py-1 rounded-md ${App.ui.computersView === "cards" ? "bg-surface-container-lowest text-primary shadow-sm" : "text-outline"}" title="Kart">${icon("grid_view", "text-[18px]")}</button>
            </div>
          </div>
        </div>
      </section>
      <section id="computers-body">${
        !rows.length
          ? `<div class="${UI.card} p-12 text-center flex flex-col items-center"><div class="w-14 h-14 rounded-full bg-surface-container-high text-outline flex items-center justify-center mb-3">${icon("devices", "text-[30px]")}</div>
              <h3 class="font-headline-sm text-headline-sm font-bold text-on-surface">${list.length ? "Filtreye uyan bilgisayar yok" : App.ui.showArchived ? "Arşivde kayıt yok" : "Henüz yedeklenen bilgisayar yok"}</h3>
              <p class="font-body-md text-body-md text-outline mt-1 max-w-md">${list.length ? "Filtreleri değiştirmeyi dene." : "İlk bilgisayarı eklemek için aşağıdaki düğmeyi kullan."}</p>
              ${list.length ? "" : `<button type="button" id="btn-add-empty" class="${UI.btnPrimary} mt-4">${icon("add", "text-[18px]")}<span>Yeni bilgisayar ekle</span></button>`}</div>`
          : App.ui.computersView === "cards"
          ? `<div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">${rows.map(computerCard).join("")}</div>`
          : `<div class="${UI.card} overflow-x-auto"><table class="w-full text-left border-collapse"><thead><tr class="bg-surface-container-low border-b border-outline-variant"><th class="${UI.th} w-8"></th><th class="${UI.th}">Bilgisayar</th><th class="${UI.th}">Şube</th><th class="${UI.th}">Departman</th><th class="${UI.th}">Durum</th><th class="${UI.th}">Güncel işlem</th><th class="${UI.th}">Son başarılı yedek</th><th class="${UI.th}">Korunan veri</th><th class="${UI.th}"></th></tr></thead><tbody class="divide-y divide-outline-variant/60 font-body-md text-body-md">${rows.map(computerRow).join("")}</tbody></table></div>`
      }</section>`;

    const redraw = () => Pages["bilgisayarlar"].render(view, params, query, { live: true });
    $("#f-q").oninput = (e) => {
      F.q = e.target.value;
      redraw().then(() => {
        const i = $("#f-q");
        i.focus();
        i.setSelectionRange(i.value.length, i.value.length);
      });
    };
    $("#f-site").onchange = (e) => ((F.site = e.target.value), redraw());
    $("#f-dept").onchange = (e) => ((F.dept = e.target.value), redraw());
    $("#f-status").onchange = (e) => ((F.status = e.target.value), redraw());
    $("#f-archived").onchange = (e) => ((App.ui.showArchived = e.target.checked), redraw());
    $$("[data-view]", view).forEach((b) => (b.onclick = () => ((App.ui.computersView = b.getAttribute("data-view")), redraw())));
    const add = $("#btn-add");
    if (add) add.onclick = () => openAddComputer();
    const addEmpty = $("#btn-add-empty");
    if (addEmpty) addEmpty.onclick = () => openAddComputer();
    $$("tr[data-href]", view).forEach((tr) => {
      tr.onclick = (e) => {
        if (e.target.closest("[data-row-menu]")) return;
        location.hash = tr.getAttribute("data-href");
      };
    });
    $$("[data-row-menu]", view).forEach((b) => attachMenu(b, computerMenuItems(list.find((d) => d.id === b.getAttribute("data-row-menu")))));
    $$("[data-card-backup]", view).forEach((b) => (b.onclick = () => runBackup(list.find((d) => d.id === b.getAttribute("data-card-backup")))));
  },
};
