"use strict";
/* Bilgisayar detay sayfası ve sekmeleri. */

const DEVICE_TABS = [
  ["genel", "Genel"],
  ["korunan", "Korunan Dosyalar"],
  ["yedekler", "Yedekler"],
  ["dosyalar", "Yedeklenen Dosyalar"],
  ["zamanlama", "Zamanlama"],
  ["sifreler", "Şifreler"],
  ["geri-yukleme", "Geri Yükleme"],
  ["olaylar", "Olaylar"],
];

const browseState = {};
const snapState = {};

function deviceView(d) {
  const st = d.status || {};
  return { ...d.device, backing_up: !!st.backing_up, backup_started_at: st.backup_started_at, run_limit_kibps: st.run_limit_kibps, site_policy: st.site_policy, next_eligible_run: st.next_eligible_run, health: st.health || d.device.health };
}

function deviceBytes(dv) {
  const u = App.state.usage;
  const x = u && u.available && u.devices ? u.devices[dv.id] : null;
  return x && x.bytes != null ? x.bytes : null;
}

const HINTS = {
  CANCELLED: "Bir sonraki pencerede aynı iş kaldığı yerden devam eder. İstersen “Şimdi yedekle” ile hemen başlatabilirsin.",
  PC_OFFLINE: "Bilgisayarı açın ve Stowline servisinin çalıştığından emin olun. Bağlantı gelince yedekleme kendiliğinden sürer.",
  SERVICE_STOPPED: "Bilgisayarda Windows Hizmetleri'nden StowlineBackup servisini başlatın.",
  WINDOW_MISSED: "Bilgisayar açık kaldığında bir sonraki uygun pencerede telafi edilir.",
  NO_WAN_ADMISSION: "Şube ölçümünü ve “Yeni yedeklemeleri durdur” durumunu Ayarlar'dan kontrol edin.",
  VSS: "Outlook açık kalabilir; sorun sürerse teknik ayrıntıyı BT'ye iletin.",
  PST_LOCKED: "Outlook'u açık bırakın; gölge kopyayı servis alır. Sorun sürerse teknik ayrıntıyı iletin.",
  DISK: "Bilgisayarda boş alanı ve kaynak klasörlerin varlığını kontrol edin.",
  AUTH: "Cihaz kimlik bilgisi reddedildi. Yeni bir kayıt gerekebilir.",
  PROVIDER: "Depolama veya ağ geçidi geçici olarak yanıt vermiyor; bir süre sonra kendiliğinden yeniden dener.",
};

function heroCard(d, dv) {
  const st = d.status || {};
  const err = st.last_error_human || {};
  const bytes = deviceBytes(dv);
  if (st.backing_up) {
    const pct = progressPct(st);
    return `<div class="rounded-xl border border-secondary/30 p-5 shadow-sm bg-gradient-to-br from-surface-container-lowest to-blue-50/60">
      <div class="flex flex-col md:flex-row md:items-center justify-between gap-3">
        <div class="flex items-center gap-3">
          <div class="w-11 h-11 rounded-xl bg-secondary/10 text-secondary flex items-center justify-center">${icon("cloud_upload", "text-[26px]")}</div>
          <div><div class="flex items-center gap-2"><h2 class="font-headline-md text-headline-md font-bold text-on-surface">Yedekleniyor</h2>${pill("live", "Canlı", true)}</div>
          <p class="font-body-md text-body-md text-outline mt-0.5">Başladı <strong class="text-on-surface">${esc(fmtTime(st.backup_started_at))}</strong> · <strong class="text-on-surface">${esc(elapsed(st.backup_started_at))}</strong> geçti</p></div>
        </div>
        <div class="flex flex-wrap items-center gap-x-5 gap-y-1 font-code-sm text-code-sm text-on-surface">
          ${bytes != null ? `<span>Bulutta <strong class="text-primary font-semibold">${esc(fmtBytes(bytes))}</strong></span>` : ""}
          <span>Yükleme sınırı <strong class="text-primary font-semibold">${esc(deviceLimitText(dv))}</strong></span>
        </div>
      </div>
      <div class="mt-4 flex items-center justify-between font-label-md text-label-md"><span class="text-outline">İlerleme</span><strong class="text-secondary">${esc(progressLabel(st))}</strong></div>
      <div class="mt-1 w-full h-3 bg-surface-container-highest rounded-full overflow-hidden p-0.5"><div class="h-full rounded-full bg-secondary ${pct == null ? "animate-progress-stripe" : "transition-[width] duration-500"}" style="width:${esc(progressWidth(st))}"></div></div>
      ${progressBytesText(st) ? `<div class="mt-1.5 flex items-center justify-between font-code-sm text-code-sm text-outline"><span>${esc(progressBytesText(st))}</span><span>${esc(progressEtaText(dv.id, st))}</span></div>` : ""}
      ${pendingLimitHtml(dv) ? `<div class="mt-3">${pendingLimitHtml(dv)}</div>` : ""}
      <p class="mt-2 font-body-sm text-body-sm text-outline">${
        progressOldAgentMessage(st)
          ? esc(progressOldAgentMessage(st))
          : st.current_progress && st.current_progress.phase === "FINALIZING"
            ? "Tüm dosyalar okundu; şifreli veri şimdi buluta yükleniyor. Yükleme sınırı yüzünden bu son aşama birkaç dakika sürebilir."
            : pct == null
            ? "Süre hesaplanıyor…"
            : `Restic'in taradığı toplam veriye göre gerçek ilerleme: %${Math.round(pct)}.`
      } Yedekleme sürerken bilgisayar komut dinlemez; gezinme ve iptal komutları yedekleme bitince işlenir.</p>
    </div>`;
  }
  if (st.health === "OFFLINE" || !st.online) {
    return `<div class="rounded-xl border border-red-200 bg-red-50/50 p-5 shadow-sm flex items-start gap-3">
      <div class="w-11 h-11 rounded-xl bg-red-100 text-red-700 flex items-center justify-center shrink-0">${icon("power_off", "text-[26px]")}</div>
      <div><h2 class="font-headline-md text-headline-md font-bold text-on-surface">Bilgisayar çevrimdışı</h2>
      <p class="font-body-md text-body-md text-on-surface-variant mt-0.5">Son sinyal <strong>${esc(fmt(st.last_heartbeat))}</strong> (${esc(ago(st.last_heartbeat))}). ${esc(HINTS.PC_OFFLINE)}</p></div></div>`;
  }
  if (err.code) {
    return `<div class="rounded-xl border border-amber-300 p-5 shadow-sm bg-gradient-to-br from-surface-container-lowest to-amber-50/50">
      <div class="flex items-start gap-3">
        <div class="w-11 h-11 rounded-xl bg-amber-100 border border-amber-300 text-amber-700 flex items-center justify-center shrink-0">${icon("report_problem", "text-[26px]")}</div>
        <div class="flex-1 min-w-0"><h2 class="font-headline-md text-headline-md font-bold text-amber-900">${esc(err.title)}</h2>
          <p class="font-body-md text-body-md text-on-surface-variant mt-1 leading-relaxed">${esc(err.summary)}</p>
          ${HINTS[err.code] ? `<div class="mt-3 p-3 rounded-lg bg-surface-container-lowest border border-amber-200"><div class="font-label-md text-label-md font-bold text-on-surface mb-0.5">Ne yapmalıyım?</div><p class="font-body-md text-body-md text-on-surface-variant">${esc(HINTS[err.code])}</p></div>` : ""}
          <details class="mt-3"><summary class="cursor-pointer font-label-md text-label-md text-secondary">Teknik ayrıntı</summary><pre class="mt-2 p-3 rounded-lg bg-surface-container-low border border-outline-variant font-code-sm text-code-sm overflow-auto">${esc(err.technical || st.last_error_code || "")}</pre></details>
        </div></div></div>`;
  }
  if (st.health === "HEALTHY") {
    return `<div class="rounded-xl border border-emerald-200 bg-emerald-50/40 p-5 shadow-sm flex items-start gap-3">
      <div class="w-11 h-11 rounded-xl bg-emerald-100 text-emerald-700 flex items-center justify-center shrink-0">${icon("check_circle", "text-[26px] fill")}</div>
      <div><h2 class="font-headline-md text-headline-md font-bold text-on-surface">Sağlıklı</h2>
      <p class="font-body-md text-body-md text-on-surface-variant mt-0.5">Son yedek <strong>${esc(ago(dv.last_success_at))}</strong> (${esc(fmt(dv.last_success_at))})${st.last_successful_snapshot ? ` · snapshot <span class="font-code-sm text-code-sm">${esc(String(st.last_successful_snapshot).slice(0, 12))}</span>` : ""}</p></div></div>`;
  }
  return `<div class="${UI.card} p-5 flex items-start gap-3">
    <div class="w-11 h-11 rounded-xl bg-surface-container-high text-outline flex items-center justify-center shrink-0">${icon("schedule", "text-[26px]")}</div>
    <div><h2 class="font-headline-md text-headline-md font-bold text-on-surface">${esc(statusOf(dv).label)}</h2>
    <p class="font-body-md text-body-md text-outline mt-0.5">${st.next_eligible_run ? `Sıradaki zamanlanmış çalışma: <strong class="text-on-surface">${esc(fmt(st.next_eligible_run))}</strong>.` : "Zamanlanmış çalışma yok."} İstersen “Şimdi yedekle” ile hemen başlatabilirsin.</p></div></div>`;
}

function kpi(label, value, sub = "") {
  return `<div class="${UI.card} p-4"><div class="font-label-md text-label-md text-outline">${esc(label)}</div><div class="mt-1.5 font-headline-sm text-headline-sm font-bold text-on-surface leading-snug">${value}</div>${sub ? `<div class="mt-1 font-body-sm text-body-sm text-outline">${sub}</div>` : ""}</div>`;
}

function renderGenelTab(body, d, dv) {
  const st = d.status || {};
  const bytes = deviceBytes(dv);
  const attempts = (d.attempts || []).slice(0, 8);
  const v = d.vault || {};
  const roots = (d.selection && d.selection.manifest && d.selection.manifest.source_roots) || [];
  body.innerHTML = `${heroCard(d, dv)}
    <div class="grid grid-cols-2 lg:grid-cols-6 gap-4 mt-6">
      ${kpi("Son başarılı yedek", esc(fmt(dv.last_success_at)), esc(dv.last_success_at ? ago(dv.last_success_at) : "henüz yok"))}
      ${kpi("Sıradaki yedek", esc(fmt(st.next_eligible_run)), esc(st.preferred_schedule ? "tercih " + st.preferred_schedule : ""))}
      ${
        st.selected_source_bytes
          ? kpi("Korunan veri", esc(fmtBytes(st.selected_source_bytes)), `${fmtNum(st.selected_source_file_count || 0)} dosya · ${st.selection_applied_locally ? "PC'de uygulandı" : "sadece panelde"}`)
          : kpi("Korunan yerler", esc(String(roots.length)), `klasör / dosya · ${st.selection_applied_locally ? "PC'de uygulandı" : "sadece panelde"}`)
      }
      ${kpi("Buluta yüklenen", bytes != null ? esc(fmtBytes(bytes)) : "—", dv.backing_up ? "şu ana kadar · sıkıştırılmış, şifreli" : "sıkıştırılmış, şifreli")}
      ${kpi("Ortalama hız", st.effective_upload_kibps_observed ? esc(fmtNum(st.effective_upload_kibps_observed)) + " KiB/sn" : "—", "son başarılı yedek")}
      ${kpi("Yedekleme penceresi", esc(windowText(dv)), esc((dv.site_policy && dv.site_policy.timezone) || ""))}
    </div>
    <div class="grid grid-cols-1 lg:grid-cols-12 gap-6 mt-6">
      <div class="lg:col-span-8 ${UI.card} p-5">
        <div class="flex items-center justify-between mb-3"><h3 class="font-headline-sm text-headline-sm font-bold text-on-surface flex items-center gap-2">${icon("timeline", "text-primary")}Son denemeler</h3><a class="text-secondary hover:underline font-label-md text-label-md" href="#/device/${esc(dv.id)}/olaylar">Tümü</a></div>
        ${
          attempts.length
            ? `<ol class="relative border-l border-outline-variant ml-2 space-y-4">${attempts
                .map(
                  (a) => `<li class="ml-4"><span class="absolute -left-[5px] w-2.5 h-2.5 rounded-full ${TONES[(OUTCOME[a.outcome] || OUTCOME.CANCELLED).tone].dot}"></span>
                    <div class="flex flex-wrap items-center gap-2">${outcomePill(a.outcome)}<span class="font-code-sm text-code-sm text-on-surface">${esc(fmt(a.ended_at))}</span>${attemptError(a) ? `<span class="font-body-sm text-body-sm text-outline" title="${esc(a.error_class)}">${esc(attemptError(a))}</span>` : ""}${a.snapshot_id ? `<span class="font-code-sm text-code-sm text-outline">snapshot ${esc(a.snapshot_id.slice(0, 12))}</span>` : ""}</div></li>`
                )
                .join("")}</ol>`
            : `<div class="py-6 text-center text-outline">Henüz tamamlanmış bir deneme yok.</div>`
        }
      </div>
      <div class="lg:col-span-4 ${UI.card} p-5">
        <h3 class="font-headline-sm text-headline-sm font-bold text-on-surface flex items-center gap-2">${icon("lock", "text-primary")}Yedekleme anahtarı</h3>
        ${
          v.has_key
            ? `<div class="mt-3 flex items-center gap-2">${pill("ok", "Kayıtlı ✓")}</div><p class="mt-2 font-body-sm text-body-sm text-outline">Parmak izi <span class="font-code-sm text-code-sm text-on-surface">${esc(v.fingerprint)}</span> · ${esc(ago(v.updated_at))}</p>`
            : `<div class="mt-3 flex items-center gap-2">${pill("bad", "Kayıtlı değil ⚠")}</div><p class="mt-2 font-body-sm text-body-sm text-outline">Bilgisayar yanarsa yedekler anahtarsız açılamaz. Anahtarı kasaya kaydedin.</p>`
        }
        <a href="#/device/${esc(dv.id)}/sifreler" class="${UI.btnSecondary} mt-4 w-full">${icon("key", "text-[18px]")}<span>Şifreler sekmesine git</span></a>
      </div>
    </div>`;
}

/* ---------- Korunan Dosyalar ---------- */
function renderKorunanTab(body, d, dv) {
  const id = dv.id;
  const roots = (d.selection && d.selection.manifest && d.selection.manifest.source_roots) || [];
  browseState[id] = browseState[id] || { path: "", entries: [], picked: new Set(roots), msg: "", cached: false, scannedAt: "" };
  const s = browseState[id];
  const busy = dv.backing_up;
  body.innerHTML = `${
    busy
      ? `<div class="mb-4 rounded-lg border border-blue-200 bg-blue-50 text-blue-900 px-4 py-3 font-body-md text-body-md">Yedekleme sürerken yeni tarama yapılamaz; daha önce kaydedilmiş klasör ve dosya adlarını yine görüntüleyebilirsiniz.</div>`
      : ""
  }
    <div class="${UI.card} p-5">
      <div class="flex items-center justify-between gap-3 mb-3"><h3 class="font-headline-sm text-headline-sm font-bold text-on-surface flex items-center gap-2">${icon("folder_managed", "text-primary")}Korunan klasörler</h3></div>
      <p class="font-body-md text-body-md text-outline mb-3">Bir klasör ilk kez açıldığında bilgisayar bir kez tarar; adlar panele kaydedilir. Sonraki açılışlar bilgisayara gitmeden anında gösterilir.</p>
      <form id="known-form" class="flex items-end gap-3 mb-3"><div class="flex-1"><label class="${UI.label}">Kaydedilmiş klasörlerde ara</label><input id="known-query" class="${UI.input}" placeholder="Örn. Desktop, faturalar, İstanbul"/></div><button type="submit" class="${UI.btnSecondary}">${icon("search", "text-[18px]")}<span>Ara</span></button></form>
      <div id="known-list" class="mb-4 rounded-lg border border-outline-variant bg-surface-container-low p-2 min-h-[48px]"><div class="px-2 py-1 text-outline">Kaydedilmiş klasörler yükleniyor…</div></div>
      <form id="browse-form" class="flex flex-col sm:flex-row sm:items-end gap-3"><div class="flex-1"><label class="${UI.label}">Klasör yolu</label><input id="browse-path" class="${UI.input} font-code-md text-code-md" value="${esc(s.path || "C:\\Users")}"/></div><button type="submit" class="${UI.btnPrimary}">${icon("folder_open", "text-[18px]")}<span>Kayıtlı listeyi göster</span></button><button type="button" id="browse-refresh" class="${UI.btnSecondary}" ${busy ? "disabled" : ""}>${icon("refresh", "text-[18px]")}<span>Yeniden tara</span></button></form>
      <p id="browse-msg" class="mt-2 font-body-sm text-body-sm text-outline min-h-[16px]">${esc(s.msg)}</p>
      <div id="browse-crumbs" class="flex flex-wrap items-center gap-1 my-2"></div>
      <div id="browse-list" class="border border-outline-variant rounded-lg max-h-[360px] overflow-auto"></div>
    </div>
    <div class="${UI.card} p-5 mt-6">
      <div class="flex items-center justify-between mb-3"><h3 class="font-headline-sm text-headline-sm font-bold text-on-surface">Seçili klasörler <span id="picked-count" class="font-code-sm text-code-sm text-outline"></span></h3>
        <div class="flex items-center gap-2"><span id="apply-status"></span><button type="button" id="btn-apply" class="${UI.btnPrimary}" ${busy ? "disabled" : ""}>${icon("check", "text-[18px]")}<span>Değişiklikleri uygula</span></button></div></div>
      <div id="picked-list" class="flex flex-wrap gap-2"></div>
      ${d.selection && !d.selection.applied_locally ? `<p class="mt-3 font-body-sm text-body-sm text-outline">Son kaydedilen seçim henüz bu bilgisayarda etkin değil; “Değişiklikleri uygula” komutu bilgisayara iletir.</p>` : ""}
    </div>`;

  const drawPicked = () => {
    const list = [...s.picked];
    $("#picked-count").textContent = `(${list.length})`;
    $("#picked-list").innerHTML = list.length
      ? list.map((p) => `<span class="inline-flex items-center gap-1.5 pl-3 pr-1.5 py-1 rounded-full bg-surface-container border border-outline-variant font-code-sm text-code-sm text-on-surface">${esc(p)}<button type="button" data-remove="${esc(p)}" class="w-5 h-5 rounded-full inline-flex items-center justify-center text-outline hover:bg-surface-container-high hover:text-error" aria-label="Kaldır">${icon("close", "text-[14px]")}</button></span>`).join("")
      : `<span class="text-outline">Henüz klasör seçilmedi.</span>`;
    $$("[data-remove]", body).forEach((b) => (b.onclick = () => (s.picked.delete(b.getAttribute("data-remove")), drawPicked(), drawEntries())));
  };
  const drawCrumbs = () => {
    const parts = (s.path || "").split(/[\\/]/).filter(Boolean);
    let acc = "";
    $("#browse-crumbs").innerHTML = parts
      .map((part) => {
        acc = acc ? acc + "\\" + part : part + "\\";
        return `<a data-goto="${esc(acc)}" class="cursor-pointer px-2 py-0.5 rounded bg-surface-container border border-outline-variant font-code-sm text-code-sm hover:bg-surface-container-high">${esc(part)}</a>`;
      })
      .join(`<span class="text-outline-variant">${icon("chevron_right", "text-[16px]")}</span>`);
    $$("[data-goto]", body).forEach((a) => (a.onclick = () => doBrowse(a.getAttribute("data-goto"))));
  };
  const drawEntries = () => {
    const list = $("#browse-list");
    if (!s.entries.length) {
      list.innerHTML = `<div class="p-8 text-center text-outline">Bu klasörde içerik yok veya henüz gözatılmadı.</div>`;
      return;
    }
    list.innerHTML = s.entries
      .map((e) => {
        const ico = e.type === "directory" ? "folder" : e.type === "reparse" ? "link" : "description";
        const has = s.picked.has(e.path);
        return `<div class="flex items-center gap-3 px-3 py-2 border-b border-outline-variant/60 last:border-b-0 ${e.type === "directory" ? "cursor-pointer hover:bg-surface-container-low" : ""}" data-entry="${esc(e.path)}" data-type="${esc(e.type)}">
          ${icon(ico, `text-[20px] ${e.type === "directory" ? "text-amber-500" : "text-outline"}`)}<span class="flex-1 truncate font-body-md text-body-md text-on-surface">${esc(e.name)}</span>
          ${e.type !== "file" ? `<button type="button" data-add="${esc(e.path)}" class="${has ? UI.btnSecondary : UI.btnPrimary} !py-1 !text-[12px]">${has ? "Eklendi" : "Klasör ekle"}</button>` : ""}</div>`;
      })
      .join("");
    $$("[data-entry]", list).forEach((row) => {
      row.onclick = (ev) => {
        if (ev.target.closest("[data-add]")) return;
        if (row.getAttribute("data-type") === "directory") doBrowse(row.getAttribute("data-entry"));
      };
    });
    $$("[data-add]", list).forEach((b) => (b.onclick = (ev) => (ev.stopPropagation(), s.picked.add(b.getAttribute("data-add")), drawPicked(), drawEntries())));
  };
  const applyListing = (listing, cached) => {
    s.path = listing.path;
    s.entries = listing.entries || [];
    s.cached = cached;
    s.scannedAt = listing.scanned_at || new Date().toISOString();
    s.msg = `${s.entries.length} öğe · ${cached ? "kayıtlı liste" : "yeni tarama"}${s.scannedAt ? " · " + fmt(s.scannedAt) : ""}`;
    $("#browse-msg").textContent = s.msg;
    $("#browse-path").value = s.path;
    drawCrumbs();
    drawEntries();
  };
  async function doBrowse(path, { force = false, scanOnMiss = true } = {}) {
    const msg = $("#browse-msg");
    try {
      if (!force) {
        msg.textContent = "Kaydedilmiş liste aranıyor…";
        const cached = await api(`/api/v1/admin/devices/${id}/browse-cache?scope=local&path=${encodeURIComponent(path)}`);
        if (cached.cached) {
          applyListing(cached, true);
          return;
        }
        if (!scanOnMiss) {
          msg.textContent = "Bu yol daha önce taranmamış. İlk tarama için ‘Yeniden tara’ya basın.";
          return;
        }
      }
      if (busy) {
        msg.textContent = "Kayıtlı sonuç yok; yeni tarama yedekleme bittikten sonra yapılabilir.";
        return;
      }
      msg.textContent = "İlk tarama bilgisayara gönderildi; sonuç geldiğinde adlar kalıcı olarak kaydedilecek…";
      const enq = await post(`/api/v1/admin/devices/${id}/browse-local-dir`, { path, limit: 500 });
      const res = await pollCommand(enq.command_id);
      if (!res.ok) {
        s.msg = res.state === "TIMEOUT" ? "Bilgisayar zamanında yanıt vermedi." : "Komut başarısız: " + res.state;
        msg.textContent = s.msg;
        return;
      }
      applyListing(res.result, false);
      loadKnown($("#known-query").value.trim());
    } catch (e) {
      msg.textContent = e.message;
    }
  }
  async function loadKnown(query = "") {
    const target = $("#known-list");
    try {
      const found = await api(`/api/v1/admin/devices/${id}/browse-cache/search?scope=local&q=${encodeURIComponent(query)}`);
      const folders = (found.matches || []).filter((entry) => ["directory", "dir"].includes(entry.type));
      target.innerHTML = folders.length
        ? `<div class="flex flex-wrap gap-2">${folders.map((entry) => `<button type="button" data-known-path="${esc(entry.path)}" class="inline-flex items-center gap-1.5 px-2.5 py-1.5 rounded-lg bg-surface-container-lowest border border-outline-variant hover:border-secondary text-left">${icon("folder", "text-[17px] text-amber-500")}<span><span class="block font-body-md text-body-md text-on-surface">${esc(entry.name)}</span><span class="block max-w-[280px] truncate font-code-sm text-code-sm text-outline">${esc(entry.path)}</span></span></button>`).join("")}</div><p class="px-1 pt-2 font-body-sm text-body-sm text-outline">${found.scanned_folders} klasör taraması panelde kayıtlı${found.truncated ? " · ilk 200 sonuç" : ""}.</p>`
        : `<div class="px-2 py-1 text-outline">${query ? "Bu adla eşleşen kayıtlı klasör yok." : "Henüz kayıtlı klasör adı yok. Bir yolu ilk kez taradığınızda burada saklanır."}</div>`;
      $$("[data-known-path]", target).forEach((button) => (button.onclick = () => doBrowse(button.getAttribute("data-known-path"))));
    } catch (e) {
      target.innerHTML = `<div class="px-2 py-1 text-error">${esc(e.message)}</div>`;
    }
  }
  $("#browse-form").onsubmit = (e) => (e.preventDefault(), doBrowse($("#browse-path").value.trim()));
  $("#browse-refresh").onclick = () => doBrowse($("#browse-path").value.trim(), { force: true });
  $("#known-form").onsubmit = (e) => (e.preventDefault(), loadKnown($("#known-query").value.trim()));
  $("#btn-apply").onclick = async () => {
    const roots2 = [...s.picked];
    if (!roots2.length) return toast("En az bir klasör seç.", "bad");
    const st = $("#apply-status");
    st.innerHTML = pill("warn", "Bekliyor");
    try {
      const enq = await post(`/api/v1/admin/devices/${id}/apply-selection`, { source_roots: roots2 });
      st.innerHTML = pill("warn", "Uygulanıyor");
      const res = await pollCommand(enq.command_id);
      st.innerHTML = res.ok ? pill("ok", "Uygulandı") : pill("bad", res.state === "TIMEOUT" ? "Yanıt yok" : "Hata");
      if (res.ok) toast("Korunan klasörler bilgisayara uygulandı.");
    } catch (e) {
      st.innerHTML = pill("bad", "Hata");
      toast(/sensitive|consent/.test(e.message) ? "Hassas dosya onayı gerekiyor." : e.message, "bad");
    }
  };
  drawPicked();
  drawCrumbs();
  drawEntries();
  loadKnown();
  if (s.path && !s.entries.length) doBrowse(s.path, { scanOnMiss: false });
}

/* ---------- Yedekler ---------- */
const fmtEntrySize = (b) => (b == null ? "—" : b < 1024 ? b + " B" : b < 1048576 ? (b / 1024).toFixed(1).replace(".", DEC_SEP) + " KB" : fmtBytes(b));

function renderYedeklerTab(body, d, dv) {
  const id = dv.id;
  const snaps = (d.attempts || []).filter((a) => a.snapshot_id);
  body.innerHTML = `<div class="${UI.card} p-5">
      <div class="flex items-center justify-between mb-3"><h3 class="font-headline-sm text-headline-sm font-bold text-on-surface flex items-center gap-2">${icon("cloud_sync", "text-primary")}Yedekler</h3>${snaps.length ? `<button type="button" id="btn-backfill-all" class="${UI.btnSecondary} !py-1.5">${icon("refresh", "text-[16px]")}<span>Eksik katalogları hazırla</span></button>` : ""}</div>
      <p class="font-body-md text-body-md text-outline mb-3">Dosya listesi, her yedeklemeden sonra bilgisayardan alınan kalıcı bir katalogdan gösterilir; bilgisayar kapalıyken de çalışır. Katalogu olmayan eski yedekler için "Eksik katalogları hazırla" kullanılabilir.</p>
      ${
        snaps.length
          ? `<div class="overflow-x-auto"><table class="w-full text-left border-collapse"><thead><tr class="bg-surface-container-low border-y border-outline-variant"><th class="${UI.th}">Tarih</th><th class="${UI.th}">Sonuç</th><th class="${UI.th}">Tutarlılık</th><th class="${UI.th}">Snapshot</th><th class="${UI.th}"></th></tr></thead><tbody class="divide-y divide-outline-variant/60">${snaps
              .map(
                (a) => `<tr class="hover:bg-surface-container-low/70"><td class="${UI.td} font-code-sm text-code-sm">${esc(fmt(a.ended_at))}</td><td class="${UI.td}">${outcomePill(a.outcome)}</td><td class="${UI.td}">${esc(a.consistency || "—")}</td><td class="${UI.td} font-code-sm text-code-sm">${esc(a.snapshot_id.slice(0, 12))}</td>
                  <td class="${UI.td} text-right whitespace-nowrap"><button type="button" data-view="${esc(a.snapshot_id)}" class="${UI.btnSecondary}" ${dv.backing_up ? "disabled title=\"Yedekleme bitince dosyalar görüntülenebilir\"" : ""}>${icon("folder_open", "text-[16px]")}<span>Dosyaları gör</span></button> <a href="#/device/${esc(id)}/geri-yukleme?snapshot=${esc(a.snapshot_id)}" class="${UI.btnPrimary} !py-1.5">${icon("settings_backup_restore", "text-[16px]")}<span>Geri yükle</span></a></td></tr>`
              )
              .join("")}</tbody></table></div>`
          : `<div class="py-10 text-center flex flex-col items-center"><div class="w-14 h-14 rounded-full bg-surface-container-high text-outline flex items-center justify-center mb-3">${icon("cloud_off", "text-[30px]")}</div><h4 class="font-headline-sm text-headline-sm font-bold text-on-surface">Henüz tamamlanmış yedek yok</h4><p class="font-body-md text-body-md text-outline mt-1">İlk yedekleme bitince burada görünür.</p></div>`
      }
      ${dv.backing_up && snaps.length ? `<p class="mt-3 font-body-sm text-body-sm text-outline">Yedekleme sürerken dosya listesi alınamaz.</p>` : ""}
    </div>
    <div id="snap-browser" class="mt-6"></div>`;
  $$("[data-view]", body).forEach((b) => (b.onclick = () => browseSnapshot(id, b.getAttribute("data-view"))));
  const backfillAll = $("#btn-backfill-all", body);
  if (backfillAll) backfillAll.onclick = () => requestCatalogBackfill(id);
}

// browseSnapshot reads the permanent, agent-built catalog first (see
// docs/superpowers/specs/2026-09-22-snapshot-catalog-design.md). A READY
// catalog renders instantly and never contacts the agent, even while it's
// offline. Only a FAILED/missing catalog (an old snapshot from before this
// feature, or one that failed to build) falls back to the old
// BROWSE_SNAPSHOT command flow, and only when the operator explicitly asks
// for it via "Dosyaları getir (tanılama)" -- never automatically.
async function browseSnapshot(id, snapshotId, prefix = "") {
  const el = $("#snap-browser");
  el.innerHTML = `<div class="${UI.card} p-5 text-outline">Katalog aranıyor…</div>`;
  try {
    const cat = await api(`/api/v1/admin/devices/${id}/snapshot-catalog?snapshot_id=${encodeURIComponent(snapshotId)}&path=${encodeURIComponent(prefix)}`);
    if (cat.state === "READY") {
      snapState[snapshotId] = {
        prefix: cat.path != null ? cat.path : prefix,
        entries: cat.entries || [],
        truncated: !!cat.truncated,
        selected: (snapState[snapshotId] && snapState[snapshotId].selected) || new Set(),
        cached: true,
        scannedAt: cat.generated_at,
      };
      drawSnapBrowser(id, snapshotId);
      return;
    }
    if (cat.state === "PENDING" || cat.state === "UPLOADING") {
      el.innerHTML = `<div class="${UI.card} p-5 text-outline">${icon("pending_actions", "text-[18px] mr-1 align-middle")}Katalog hazırlanıyor. Bu yedeklemenin bitmesinden hemen sonra kısa bir süre sürer; sayfayı biraz sonra tekrar açabilirsiniz.</div>`;
      return;
    }
    el.innerHTML = `<div class="${UI.card} p-5">
      <p class="text-outline mb-3">${cat.state === "FAILED" ? "Bu yedek için katalog oluşturulamadı." : "Bu yedek için katalog yok (özellik eklenmeden önce alınmış olabilir)."}</p>
      <div class="flex flex-wrap gap-2">
        <button type="button" id="btn-diag-browse" class="${UI.btnSecondary}">${icon("search", "text-[16px]")}<span>Dosyaları getir (tanılama)</span></button>
        <button type="button" id="btn-backfill-one" class="${UI.btnSecondary}">${icon("refresh", "text-[16px]")}<span>Eksik katalogları hazırla</span></button>
      </div></div>`;
    $("#btn-diag-browse").onclick = () => browseSnapshotDiagnostic(id, snapshotId, prefix);
    $("#btn-backfill-one").onclick = () => requestCatalogBackfill(id);
  } catch (e) {
    el.innerHTML = `<div class="${UI.card} p-5 text-error">${esc(e.message)}</div>`;
  }
}

// browseSnapshotDiagnostic is the pre-catalog behavior, kept verbatim as an
// explicit, one-off fallback for a snapshot with no READY catalog. It is
// never called automatically.
async function browseSnapshotDiagnostic(id, snapshotId, prefix = "") {
  const el = $("#snap-browser");
  el.innerHTML = `<div class="${UI.card} p-5 text-outline">Kayıtlı liste aranıyor…</div>`;
  try {
    const cached = await api(`/api/v1/admin/devices/${id}/browse-cache?scope=snapshot&snapshot_id=${encodeURIComponent(snapshotId)}&path=${encodeURIComponent(prefix)}`);
    if (cached.cached) {
      snapState[snapshotId] = {
        prefix: cached.path != null ? cached.path : prefix,
        entries: cached.entries || [],
        truncated: !!cached.truncated,
        selected: (snapState[snapshotId] && snapState[snapshotId].selected) || new Set(),
        cached: true,
        scannedAt: cached.scanned_at,
      };
      drawSnapBrowser(id, snapshotId);
      return;
    }
    el.innerHTML = `<div class="${UI.card} p-5 text-outline">Bu klasör ilk kez taranıyor; sonuç daha sonraki açılışlar için kaydedilecek…</div>`;
    const enq = await post(`/api/v1/admin/devices/${id}/browse`, { snapshot_id: snapshotId, prefix });
    // Restic ls walks the selected subtree before the agent reduces it to a
    // single directory level. Real PST-heavy snapshots can legitimately take
    // longer than the generic 90-second command wait, even though the command
    // is healthy and eventually succeeds.
    const res = await pollCommand(enq.command_id, { timeoutMs: 300000, intervalMs: 1500 });
    if (!res.ok) {
      el.innerHTML = `<div class="${UI.card} p-5 text-error">${res.state === "TIMEOUT" ? "Bilgisayar zamanında yanıt vermedi." : "Komut başarısız: " + esc(res.state)}</div>`;
      return;
    }
    snapState[snapshotId] = { prefix: res.result.prefix != null ? res.result.prefix : prefix, entries: res.result.entries || [], truncated: !!res.result.truncated, selected: new Set(), cached: false, scannedAt: new Date().toISOString() };
    drawSnapBrowser(id, snapshotId);
  } catch (e) {
    el.innerHTML = `<div class="${UI.card} p-5 text-error">${esc(e.message)}</div>`;
  }
}

async function requestCatalogBackfill(id) {
  try {
    const res = await post(`/api/v1/admin/devices/${id}/backfill-catalogs`, {});
    toast(res.command_ids && res.command_ids.length ? "Eksik kataloglar için istek gönderildi; biraz sonra tekrar açın." : "Eksik katalog bulunamadı; hepsi zaten hazır.");
  } catch (e) {
    toast(e.message, "bad");
  }
}

function drawSnapBrowser(id, snapshotId) {
  const el = $("#snap-browser");
  const st = snapState[snapshotId];
  const parts = (st.prefix || "").split("/").filter(Boolean);
  let acc = "";
  const crumbs = [{ label: "Kök", path: "" }, ...parts.map((p) => ((acc += "/" + p), { label: p, path: acc }))];
  const rows = st.entries.slice().sort((a, b) => ((a.type === "dir") !== (b.type === "dir") ? (a.type === "dir" ? -1 : 1) : (a.name || "").localeCompare(b.name || "", "tr")));
  el.innerHTML = `<div class="${UI.card} p-5">
    <div class="flex items-center justify-between mb-2"><div><h3 class="font-headline-sm text-headline-sm font-bold text-on-surface">Snapshot <span class="font-code-md text-code-md">${esc(snapshotId.slice(0, 12))}</span></h3><p class="font-body-sm text-body-sm text-outline">${st.cached ? "Panelde kayıtlı liste" : "Yeni tarandı ve panele kaydedildi"}${st.scannedAt ? " · " + esc(fmt(st.scannedAt)) : ""}</p></div>
      <div id="snap-actions"></div></div>
    <div class="flex flex-wrap items-center gap-1 mb-3">${crumbs.map((c, i) => (i === crumbs.length - 1 ? `<span class="px-2 py-0.5 rounded bg-primary text-on-primary font-code-sm text-code-sm">${esc(c.label)}</span>` : `<a data-goto="${esc(c.path)}" class="cursor-pointer px-2 py-0.5 rounded bg-surface-container border border-outline-variant font-code-sm text-code-sm hover:bg-surface-container-high">${esc(c.label)}</a>`)).join(`<span class="text-outline-variant">${icon("chevron_right", "text-[16px]")}</span>`)}</div>
    <div class="border border-outline-variant rounded-lg overflow-hidden">
      <div class="grid grid-cols-[28px_minmax(0,1fr)_110px_150px_72px] gap-3 px-3 py-2 bg-surface-container-low border-b border-outline-variant font-label-sm text-label-sm text-outline uppercase tracking-wider"><span></span><span>Ad</span><span>Boyut</span><span>Değiştirilme</span><span></span></div>
      <div class="max-h-[420px] overflow-auto">${
        rows.length
          ? rows
              .map((e) => {
                const isDir = e.type === "dir";
                return `<div class="grid grid-cols-[28px_minmax(0,1fr)_110px_150px_72px] gap-3 items-center px-3 py-2 border-b border-outline-variant/60 last:border-b-0 ${isDir ? "cursor-pointer hover:bg-surface-container-low" : ""}" data-entry="${esc(e.path)}" data-dir="${isDir ? "1" : ""}">
                  <input type="checkbox" data-pick="${esc(e.path)}" class="rounded border-outline-variant text-secondary focus:ring-secondary/30" ${st.selected.has(e.path) ? "checked" : ""}/>
                  <span class="flex items-center gap-2 truncate">${icon(isDir ? "folder" : e.type === "symlink" ? "link" : "description", `text-[20px] ${isDir ? "text-amber-500" : "text-outline"}`)}<span class="truncate font-body-md text-body-md text-on-surface">${esc(e.name)}</span></span>
                  <span class="font-code-sm text-code-sm text-outline">${isDir ? "" : esc(fmtEntrySize(e.size))}</span><span class="font-code-sm text-code-sm text-outline">${isDir ? "" : esc(fmt(e.mtime))}</span><span class="font-label-sm text-label-sm font-semibold text-secondary">${isDir ? "Aç ›" : ""}</span></div>`;
              })
              .join("")
          : `<div class="p-8 text-center text-outline">Boş</div>`
      }</div></div>
    ${st.truncated ? `<p class="mt-2 font-body-sm text-body-sm text-outline">Liste çok büyük; ilk sonuçlar gösteriliyor.</p>` : ""}
    <p class="mt-3 font-body-sm text-body-sm text-outline">Tek bir dosyanın kutusunu işaretleyebilirsiniz; yalnızca seçtiğiniz dosya indirilir, tüm yedeği indirmeniz gerekmez. Geri yükleme yalnızca onaylı bekleme (staging) alanına yazar; orijinal dosyalar asla ezilmez.</p></div>`;
  const drawActions = () => {
    const n = st.selected.size;
    $("#snap-actions").innerHTML = n ? `<a href="#/device/${esc(id)}/geri-yukleme?snapshot=${esc(snapshotId)}&paths=${encodeURIComponent([...st.selected].join("\n"))}" class="${UI.btnPrimary}">${icon("settings_backup_restore", "text-[18px]")}<span>Bekleme alanına geri yükle (${n})</span></a>` : "";
  };
  drawActions();
  $$("[data-goto]", el).forEach((a) => (a.onclick = () => browseSnapshot(id, snapshotId, a.getAttribute("data-goto"))));
  $$("[data-entry]", el).forEach((row) => {
    row.onclick = (ev) => {
      if (ev.target.closest("[data-pick]")) return;
      if (row.getAttribute("data-dir")) browseSnapshot(id, snapshotId, row.getAttribute("data-entry"));
    };
  });
  $$("[data-pick]", el).forEach((c) => {
    c.onchange = () => {
      if (c.checked) st.selected.add(c.getAttribute("data-pick"));
      else st.selected.delete(c.getAttribute("data-pick"));
      drawActions();
    };
  });
}

/* ---------- Yedeklenen Dosyalar ---------- */
async function renderDosyalarTab(body, d, dv) {
  const id = dv.id;
  body.innerHTML = `<div class="${UI.card} p-5">
      <h3 class="font-headline-sm text-headline-sm font-bold text-on-surface flex items-center gap-2 mb-2">${icon("search", "text-primary")}Yedeklenen Dosyalar</h3>
      <p class="font-body-md text-body-md text-outline mb-3">Bu bilgisayarın tüm yedeklerinde (snapshot'larında) dosya adına göre arama yapar; bir dosyanın hangi tarihli yedek(ler)de bulunduğunu gösterir.</p>
      <form id="files-form" class="flex items-center gap-2 mb-4"><div class="relative flex-1 max-w-md">${icon("search", "absolute left-3 top-2.5 text-[18px] text-outline")}<input id="files-q" class="${UI.input} pl-9" placeholder="Dosya adı ara" autocomplete="off"/></div><button type="submit" class="${UI.btnPrimary}">Ara</button></form>
      <div id="files-results"></div>
    </div>`;
  const drawResults = (data) => {
    const target = $("#files-results", body);
    const matches = data.matches || [];
    if (!matches.length) {
      target.innerHTML = `<div class="py-8 text-center text-outline">${data.q ? "Bu adla eşleşen dosya bulunamadı." : "Aramak için bir dosya adı yazın."}</div>`;
      return;
    }
    target.innerHTML = `<div class="overflow-x-auto"><table class="w-full text-left border-collapse"><thead><tr class="bg-surface-container-low border-y border-outline-variant"><th class="${UI.th}">Ad</th><th class="${UI.th}">Klasör</th><th class="${UI.th}">Boyut</th><th class="${UI.th}">Yedek tarihi</th><th class="${UI.th}"></th></tr></thead><tbody class="divide-y divide-outline-variant/60">${matches
      .map(
        (m) => `<tr class="hover:bg-surface-container-low/70"><td class="${UI.td} font-body-md text-body-md text-on-surface">${icon(m.type === "dir" ? "folder" : "description", `text-[16px] align-middle mr-1 ${m.type === "dir" ? "text-amber-500" : "text-outline"}`)}${esc(m.name)}</td><td class="${UI.td} font-code-sm text-code-sm text-outline truncate max-w-[280px]">${esc(m.folder)}</td><td class="${UI.td} font-code-sm text-code-sm text-outline">${fmtEntrySize(m.size)}</td><td class="${UI.td} font-code-sm text-code-sm">${esc(fmt(m.snapshot_time))}</td><td class="${UI.td} text-right"><a href="#/device/${esc(id)}/yedekler" class="${UI.btnSecondary} !py-1 !text-[12px]">${icon("arrow_forward", "text-[14px]")}<span>Yedekler'de aç</span></a></td></tr>`
      )
      .join("")}</tbody></table></div>${data.truncated ? `<p class="mt-2 font-body-sm text-body-sm text-outline">Sonuç çok; ilk eşleşmeler gösteriliyor.</p>` : ""}`;
  };
  const runSearch = async (q) => {
    const target = $("#files-results", body);
    target.innerHTML = `<div class="py-6 text-center text-outline">Aranıyor…</div>`;
    try {
      const data = await api(`/api/v1/admin/devices/${id}/files?q=${encodeURIComponent(q)}`);
      drawResults({ ...data, q });
    } catch (e) {
      target.innerHTML = `<div class="py-6 text-center text-error">${esc(e.message)}</div>`;
    }
  };
  $("#files-form", body).onsubmit = (e) => (e.preventDefault(), runSearch($("#files-q", body).value.trim()));
  drawResults({ matches: [], q: "" });
}

/* ---------- Zamanlama ---------- */
function renderZamanlamaTab(body, d, dv) {
  const st = d.status || {};
  const sp = dv.site_policy || {};
  const cur = String(dv.department || "").trim();
  const known = cur;
  const options = [...DEPARTMENTS, ...(known && !DEPARTMENTS.includes(known) ? [known] : [])];
  body.innerHTML = `<div class="grid grid-cols-1 lg:grid-cols-12 gap-6">
    <form id="sched-form" class="lg:col-span-7 ${UI.card} p-5">
      <h3 class="font-headline-sm text-headline-sm font-bold text-on-surface flex items-center gap-2 mb-4">${icon("tune", "text-primary")}Bilgisayar adı, departman ve zamanlama</h3>
      <div class="mb-4"><label class="${UI.label}">Panelde görünen bilgisayar adı</label><input id="s-name" class="${UI.input}" maxlength="64" value="${esc(dv.display_name || "")}" placeholder="${esc(dv.hostname)}"/>
        <p class="mt-1 font-body-sm text-body-sm text-outline">Boş bırakırsanız gerçek Windows adı gösterilir: <span class="font-code-sm text-code-sm text-on-surface">${esc(dv.hostname)}</span>. Bu etiket depodaki klasörü veya cihaz kimliğini değiştirmez.</p></div>
      <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
        <div><label class="${UI.label}">Departman</label><select id="s-dept" class="${UI.input}">${!known ? `<option value="" selected disabled>Atanmamış — seçin</option>` : ""}${options.map((o) => `<option value="${esc(o)}" ${o === known ? "selected" : ""}>${esc(DEPT_LABEL[o] || o)}</option>`).join("")}</select></div>
        <div><label class="${UI.label}">Tercih edilen başlangıç (${esc(sp.timezone || "Europe/Istanbul")})</label><input id="s-pref" type="time" class="${UI.input}" value="${esc(st.preferred_schedule || "12:00")}"/>
          <p class="mt-1 font-body-sm text-body-sm text-outline">İzin verilen aralık: ${esc(sp.eligibility_start || "—")} – ${esc(sp.preferred_latest_start || "—")}</p></div>
      </div>
      <p class="mt-4 font-body-sm text-body-sm text-outline">Departman değişikliği depolama konumunu asla değiştirmez; yalnızca bir etikettir.</p>
      <div class="mt-4"><button type="submit" class="${UI.btnPrimary}">${icon("save", "text-[18px]")}<span>Kaydet</span></button></div>
    </form>
    <div class="lg:col-span-5 ${UI.card} p-5">
      <h3 class="font-headline-sm text-headline-sm font-bold text-on-surface flex items-center gap-2 mb-4">${icon("domain", "text-primary")}${esc(siteLabel(dv.site_id))} şubesi kuralları</h3>
      <dl class="space-y-2 font-code-sm text-code-sm">
        <div class="flex justify-between"><dt class="text-outline">Yedekleme başlar</dt><dd class="text-on-surface">${esc(sp.eligibility_start || "—")}</dd></div>
        <div class="flex justify-between"><dt class="text-outline">En geç başlangıç</dt><dd class="text-on-surface">${esc(sp.preferred_latest_start || "—")}</dd></div>
        <div class="flex justify-between"><dt class="text-outline">Durdurma (hard stop)</dt><dd class="text-on-surface">${esc(sp.hard_stop || "—")}</dd></div>
        <div class="flex justify-between"><dt class="text-outline">Yükleme sınırı</dt><dd class="text-on-surface">${esc(deviceLimitText(dv))}</dd></div>
        <div class="flex justify-between"><dt class="text-outline">Sıradaki çalışma</dt><dd class="text-on-surface">${esc(fmt(st.next_eligible_run))}</dd></div>
      </dl>
      <p class="mt-4 font-body-sm text-body-sm text-outline">Pencere ve hız sınırı şube ayarıdır; Ayarlar sayfasından şube bazında düzenlenir. Yedek pencere sonunda durdurulur ve sonraki gün kaldığı yerden sürer.</p>
    </div></div>`;
  $("#sched-form").onsubmit = async (e) => {
    e.preventDefault();
    try {
      await patch(`/api/v1/admin/devices/${dv.id}`, { display_name: $("#s-name").value, department: $("#s-dept").value || undefined, preferred_start_hhmm: $("#s-pref").value });
      toast("Bilgisayar adı ve zamanlama kaydedildi.");
      rerender();
    } catch (err) {
      toast(err.message, "bad");
    }
  };
}

/* ---------- Geri Yükleme ---------- */
async function renderGeriYuklemeTab(body, d, dv, query) {
  const snaps = (d.attempts || []).filter((a) => a.snapshot_id);
  const preset = query.snapshot || (snaps[0] && snaps[0].snapshot_id) || dv.last_success_snapshot || "";
  const reqs = ((await api("/api/v1/admin/restore-requests")).items || []).filter((r) => r.device_id === dv.id);
  body.innerHTML = `<div class="mb-4 rounded-lg border border-amber-300 bg-amber-50 text-amber-900 px-4 py-3 font-body-md text-body-md">Geri yükleme yalnızca onaylı bekleme (staging) alanına yazılır. Orijinal dosyalar asla üzerine yazılmaz. Geri yüklenen dosyaları çalıştırmayın.</div>
    <div class="grid grid-cols-1 lg:grid-cols-12 gap-6">
      <form id="rest-form" class="lg:col-span-6 ${UI.card} p-5">
        <h3 class="font-headline-sm text-headline-sm font-bold text-on-surface flex items-center gap-2 mb-4">${icon("settings_backup_restore", "text-primary")}Sınırlı geri yükleme</h3>
        <label class="${UI.label}">Snapshot</label>
        ${snaps.length ? `<select id="r-snap" class="${UI.input} font-code-md text-code-md">${snaps.map((a) => `<option value="${esc(a.snapshot_id)}" ${a.snapshot_id === preset ? "selected" : ""}>${esc(a.snapshot_id.slice(0, 12))} · ${esc(fmt(a.ended_at))}</option>`).join("")}</select>` : `<input id="r-snap" class="${UI.input} font-code-md text-code-md" maxlength="64" placeholder="Snapshot kimliği (64 karakter)" value="${esc(preset)}"/>`}
        <label class="${UI.label} mt-4">Snapshot içi yollar (satır başına bir yol)</label>
        <textarea id="r-paths" rows="5" class="${UI.textarea} font-code-md text-code-md" placeholder="/C/Users/…">${esc(query.paths || "")}</textarea>
        <div class="mt-4"><button type="submit" class="${UI.btnPrimary}" ${dv.backing_up ? "disabled" : ""}>${icon("play_arrow", "text-[18px]")}<span>Bekleme alanına geri yükle</span></button>${dv.backing_up ? `<span class="ml-3 font-body-sm text-body-sm text-outline">Yedekleme sürerken başlatılamaz.</span>` : ""}</div>
      </form>
      <div class="lg:col-span-6 ${UI.card} p-5"><h3 class="font-headline-sm text-headline-sm font-bold text-on-surface mb-3">İstekler</h3>${
        reqs.length
          ? `<div class="overflow-x-auto"><table class="w-full text-left border-collapse"><thead><tr class="bg-surface-container-low border-y border-outline-variant"><th class="${UI.th}">Snapshot</th><th class="${UI.th}">Durum</th><th class="${UI.th}">Mod</th></tr></thead><tbody class="divide-y divide-outline-variant/60">${reqs.map((r) => `<tr><td class="${UI.td} font-code-sm text-code-sm">${esc(String(r.snapshot_id).slice(0, 12))}</td><td class="${UI.td}">${restorePill(r.state)}</td><td class="${UI.td}">${esc(RESTORE_MODE_TR[r.destination_mode] || r.destination_mode)}</td></tr>`).join("")}</tbody></table></div>`
          : `<div class="py-6 text-center text-outline">Bu bilgisayar için geri yükleme isteği yok.</div>`
      }</div></div>`;
  $("#rest-form").onsubmit = async (e) => {
    e.preventDefault();
    const selections = $("#r-paths").value.split(/\n/).map((s) => s.trim()).filter(Boolean);
    try {
      await post("/api/v1/admin/restore-requests", { device_id: dv.id, snapshot_id: $("#r-snap").value.trim(), selections });
      toast("Geri yükleme isteği kuyruğa alındı.");
      rerender();
    } catch (err) {
      toast(err.message, "bad");
    }
  };
}

/* ---------- Olaylar ---------- */
async function renderOlaylarTab(body, d, dv) {
  const events = ((await api("/api/v1/admin/audit-events")).items || []).filter((e) => e.device_id === dv.id).slice(0, 40);
  body.innerHTML = `<div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
    <div class="${UI.card} p-5"><h3 class="font-headline-sm text-headline-sm font-bold text-on-surface mb-3">Yedekleme geçmişi</h3>${
      (d.attempts || []).length
        ? `<div class="overflow-x-auto"><table class="w-full text-left border-collapse"><thead><tr class="bg-surface-container-low border-y border-outline-variant"><th class="${UI.th}">Sonuç</th><th class="${UI.th}">Hata</th><th class="${UI.th}">Tutarlılık</th><th class="${UI.th} text-right">Bitiş</th></tr></thead><tbody class="divide-y divide-outline-variant/60">${d.attempts.map((a) => `<tr><td class="${UI.td}">${outcomePill(a.outcome)}</td><td class="${UI.td}" title="${esc(a.error_class)}">${esc(attemptError(a) || "—")}</td><td class="${UI.td}">${esc(a.consistency || "—")}</td><td class="${UI.td} font-code-sm text-code-sm text-right">${esc(fmt(a.ended_at))}</td></tr>`).join("")}</tbody></table></div>`
        : `<div class="py-6 text-center text-outline">Henüz deneme yok.</div>`
    }</div>
    <div class="${UI.card} p-5"><h3 class="font-headline-sm text-headline-sm font-bold text-on-surface mb-3">Bu bilgisayarla ilgili olaylar</h3>${
      events.length
        ? `<ul class="divide-y divide-outline-variant/60">${events.map((e) => `<li class="py-2.5 flex items-center justify-between gap-3"><div><div class="font-body-md text-body-md text-on-surface">${esc(auditLabel(e))}</div><div class="font-body-sm text-body-sm text-outline">${esc(actorLabel(e.actor_type))}</div></div><div class="font-code-sm text-code-sm text-outline text-right">${esc(fmt(e.occurred_at))}${e.result && e.result !== "OK" ? `<div class="text-error font-semibold">${esc(e.result)}</div>` : ""}</div></li>`).join("")}</ul>`
        : `<div class="py-6 text-center text-outline">Olay yok.</div>`
    }</div></div>`;
}

function auditLabel(e) {
  const base = AUDIT_TR[e.action] || e.action;
  if (e.action === "user_message" && e.detail) return `${base}: “${e.detail}”`;
  if (e.action === "self_service_restore" && e.detail) return `${base} → ${e.detail}`;
  return e.detail && COMMAND_TR[e.detail] ? `${base}: ${COMMAND_TR[e.detail]}` : base;
}

/* ---------- sayfa ---------- */
Pages["device"] = {
  live: true,
  liveWhen: (route) => (route.params.tab || "genel") === "genel",
  async render(view, params, query, ctx) {
    const id = params.id;
    const tab = DEVICE_TABS.some(([k]) => k === params.tab) ? params.tab : "genel";
    const d = await api(`/api/v1/admin/devices/${id}`);
    const dv = deviceView(d);
    const st = d.status || {};
    const closed = windowClosed(dv);
    const cantRun = dv.backing_up || !!closed || dv.lifecycle !== "ACTIVE";
    const runTitle = dv.backing_up ? "Yedekleme zaten sürüyor" : closed ? `Yedekleme penceresi kapalı: ${closed}'de sona erdi` : "";
    view.innerHTML = `
      <section class="${UI.card} p-5">
        <a href="#/bilgisayarlar" class="inline-flex items-center gap-1 text-secondary hover:underline font-label-md text-label-md mb-3">${icon("arrow_back", "text-[16px]")}<span>Bilgisayarlar</span></a>
        <div class="flex flex-col lg:flex-row lg:items-start justify-between gap-4">
          <div>
            <div class="flex flex-wrap items-center gap-3"><h1 class="font-headline-lg text-headline-lg text-primary font-bold tracking-tight font-code-md">${esc(deviceName(dv))}</h1>${statusPill(dv)}</div>
            <div class="mt-2 flex flex-wrap items-center gap-2 font-body-sm text-body-sm">
              ${deviceWasRenamed(dv) ? `<span class="px-2 py-0.5 rounded-full bg-surface-container border border-outline-variant text-on-surface-variant font-code-sm text-code-sm" title="Gerçek Windows bilgisayar adı">${esc(dv.hostname)}</span>` : ""}
              <span class="px-2 py-0.5 rounded-full bg-surface-container border border-outline-variant text-on-surface-variant">${esc(siteLabel(dv.site_id))}</span>
              <span class="px-2 py-0.5 rounded-full bg-surface-container border border-outline-variant text-on-surface-variant">${esc(deptOf(dv))}</span>
              <span class="px-2 py-0.5 rounded-full bg-surface-container border border-outline-variant text-on-surface-variant font-code-sm text-code-sm" title="${esc(dv.agent_sha ? "Çalışan agent SHA-256: " + dv.agent_sha : "Agent SHA bilinmiyor")}">Agent ${esc(dv.agent_version || "—")}${dv.agent_sha ? " · " + esc(dv.agent_sha.slice(0, 8)) : ""}</span>
              <span class="px-2 py-0.5 rounded-full border ${st.online ? "bg-emerald-50 border-emerald-200 text-emerald-700" : "bg-red-50 border-red-200 text-red-700"}">${st.online ? "Çevrimiçi" : "Çevrimdışı"}</span>
            </div>
          </div>
          <div class="flex flex-wrap items-center gap-2">
            <button type="button" id="h-run" class="${UI.btnPrimary}" ${cantRun ? "disabled" : ""} title="${esc(runTitle)}">${icon("play_circle", "text-[18px]")}<span>Şimdi yedekle</span></button>
            ${dv.lifecycle === "ACTIVE" ? (dv.pause_new_backups ? `<button type="button" id="h-pause" class="${UI.btnSecondary}">${icon("play_arrow", "text-[18px]")}<span>Sürdür</span></button>` : `<button type="button" id="h-pause" class="${UI.btnSecondary}">${icon("pause", "text-[18px]")}<span>Yeni yedeklemeleri durdur</span></button>`) : ""}
            <button type="button" id="h-cancel" class="${UI.btnSecondary}" ${dv.lifecycle !== "ACTIVE" ? "disabled" : ""}>${icon("cancel", "text-[18px]")}<span>Çalışan işi iptal et</span></button>
            <button type="button" id="h-more" class="w-9 h-9 inline-flex items-center justify-center rounded-lg border border-outline-variant bg-surface-container-lowest hover:bg-surface-container text-on-surface transition-colors" aria-label="Diğer işlemler">${icon("more_horiz")}</button>
          </div>
        </div>
        <nav class="mt-5 -mb-px flex flex-wrap gap-1 border-b border-outline-variant">${DEVICE_TABS.map(([k, l]) => `<a href="#/device/${esc(id)}/${k}" class="px-4 py-2.5 font-label-md text-label-md border-b-2 transition-colors ${tab === k ? "border-secondary text-primary font-semibold" : "border-transparent text-outline hover:text-on-surface"}">${l}</a>`).join("")}</nav>
      </section>
      <div id="tab-body" class="space-y-0"></div>`;

    $("#h-run").onclick = () => runBackup(dv);
    const pauseBtn = $("#h-pause");
    if (pauseBtn)
      pauseBtn.onclick = async () => {
        try {
          await post(`/api/v1/admin/devices/${id}/${dv.pause_new_backups ? "resume" : "pause"}`);
          toast(dv.pause_new_backups ? "Yeni yedeklemeler sürdürüldü." : "Yeni yedeklemeler durduruldu. Çalışan iş etkilenmez.");
          rerender();
        } catch (e) {
          toast(e.message, "bad");
        }
      };
    $("#h-cancel").onclick = async () => {
      const ok = await confirmDialog({
        title: "Çalışan işi iptal et",
        message: dv.backing_up
          ? "Bilgisayar yedekleme sürerken komut dinlemediği için çalışan bir yedeği <strong>hemen durduramaz</strong>; komut yedekleme bitince işlenir. Hemen durdurmak için bilgisayardaki StowlineBackup servisini durdurun. Yine de komutu göndermek istiyor musunuz?"
          : "İptal komutu bilgisayara gönderilecek. Şu an çalışan bir iş görünmüyor.",
        confirmText: "Komutu gönder",
        tone: "danger",
      });
      if (!ok) return;
      try {
        await post(`/api/v1/admin/devices/${id}/commands`, { kind: "CANCEL_CURRENT_SAFE_OPERATION", payload: {} });
        toast("İptal komutu kuyruğa alındı.");
      } catch (e) {
        toast(e.message, "bad");
      }
    };
    attachMenu($("#h-more"), [
      { label: "Canary çalıştır", icon: "science", disabled: cantRun, run: async () => { try { await post(`/api/v1/admin/devices/${id}/commands`, { kind: "RUN_CANARY", payload: {} }); toast("Canary komutu kuyruğa alındı."); } catch (e) { toast(e.message, "bad"); } } },
      ...(dv.lifecycle === "ACTIVE" ? computerMenuItems(dv).filter((i) => i.label === "Arşivle") : dv.lifecycle === "ARCHIVED" ? computerMenuItems(dv).filter((i) => i.label === "Arşivden çıkar") : []),
      ...(dv.lifecycle === "ACTIVE"
        ? [{
            label: "Cihazı iptal et (revoke)", icon: "block", danger: true,
            run: async () => {
              const ok = await confirmDialog({ title: "Cihazı iptal et (revoke)", message: `<strong>${esc(deviceName(dv))}</strong> için kimlik bilgisi iptal edilecek; bilgisayar bir daha yedekleme yazamaz ve <strong>bu işlem geri alınamaz</strong> (yeniden kayıt gerekir). Geçici olarak listeden çıkarmak için “Arşivle”yi kullanın.`, confirmText: "İptal et", tone: "danger" });
              if (!ok) return;
              try { await post(`/api/v1/admin/devices/${id}/revoke`); toast("Cihaz iptal edildi."); location.hash = "#/bilgisayarlar"; } catch (e) { toast(e.message, "bad"); }
            },
          }]
        : []),
    ]);

    const body = $("#tab-body");
    body.className = "mt-6";
    if (tab === "genel") renderGenelTab(body, d, dv);
    else if (tab === "korunan") renderKorunanTab(body, d, dv);
    else if (tab === "yedekler") renderYedeklerTab(body, d, dv);
    else if (tab === "dosyalar") await renderDosyalarTab(body, d, dv);
    else if (tab === "zamanlama") renderZamanlamaTab(body, d, dv);
    else if (tab === "sifreler") renderSifrelerTab(body, d, dv);
    else if (tab === "geri-yukleme") await renderGeriYuklemeTab(body, d, dv, query);
    else if (tab === "olaylar") await renderOlaylarTab(body, d, dv);
  },
};
