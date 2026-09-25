"use strict";
/* Şifre Kasası: bilgisayarların yedekleme anahtarları (yalnızca parmak izi listelenir, anahtar şifre girilince gösterilir). */

const SOURCE_TR = { device: "Bilgisayardan (araç)", operator: "Elle girildi" };
const RESTIC_PIN = "b0dd1fd21eea5d8fe1325f55f7118213c21f36de8a261e04c0624a5ab9fd7830";

function keyGroups(secret) {
  const parts = [];
  for (let i = 0; i < secret.length; i += 8) parts.push(secret.slice(i, i + 8));
  return parts;
}

function openRevealModal(dev) {
  let secret = "";
  let timer = null;
  const m = openModal({
    title: `Anahtarı göster · ${deviceName(dev)}`,
    onClose: () => {
      secret = "";
      if (timer) clearInterval(timer);
    },
    html: `<form id="reveal-form" class="space-y-4">
      <p class="font-body-md text-body-md text-on-surface-variant">Bu anahtar yedeklerin tek açıcısıdır. Göstermek için <strong>yönetici şifrenizi</strong> tekrar girin. Bu görüntüleme denetim kaydına yazılır.</p>
      <div><label class="${UI.label}">Yönetici şifresi</label><input id="reveal-pw" type="password" class="${UI.input}" autocomplete="current-password"/></div>
      <p id="reveal-err" class="text-error font-body-md text-body-md min-h-[20px]"></p>
      <div class="flex justify-end gap-2"><button type="button" data-cancel class="${UI.btnSecondary}">Vazgeç</button><button type="submit" class="${UI.btnPrimary}">Devam</button></div>
    </form>`,
  });
  $("[data-cancel]", m.el).onclick = () => m.close();
  $("#reveal-form", m.el).onsubmit = async (e) => {
    e.preventDefault();
    const pw = $("#reveal-pw", m.el).value;
    try {
      const r = await post(`/api/v1/admin/devices/${dev.device_id || dev.id}/secret/reveal`, { password: pw });
      secret = r.secret;
      const groups = keyGroups(secret);
      let left = 30;
      m.body.innerHTML = `<div class="space-y-4">
        <div class="flex items-center justify-between"><div class="font-label-md text-label-md text-outline">Parmak izi <span class="font-code-md text-code-md text-on-surface">${esc(r.fingerprint)}</span></div>
          <div class="relative w-10 h-10"><svg viewBox="0 0 36 36" class="w-10 h-10 -rotate-90"><circle cx="18" cy="18" r="16" fill="none" stroke="#e7eeff" stroke-width="3"/><circle id="ring" cx="18" cy="18" r="16" fill="none" stroke="#004fda" stroke-width="3" stroke-linecap="round" stroke-dasharray="100.5" stroke-dashoffset="0" style="transition:stroke-dashoffset 1s linear"/></svg><span id="ring-num" class="absolute inset-0 flex items-center justify-center font-code-sm text-code-sm text-on-surface">30</span></div></div>
        <div class="grid grid-cols-2 sm:grid-cols-4 gap-2">${groups.map((g) => `<div class="px-2 py-2 rounded-lg bg-surface-container-low border border-outline-variant text-center font-code-md text-code-md font-semibold text-primary select-all">${esc(g)}</div>`).join("")}</div>
        <div class="p-3 rounded-lg bg-amber-50 border border-amber-200 text-amber-900 font-body-sm text-body-sm">Bu anahtarı e-posta, sohbet veya ekran görüntüsüyle göndermeyin. Ekran ${left} saniye sonra otomatik temizlenir.</div>
        <div class="flex justify-end gap-2"><button type="button" id="copy-key" class="${UI.btnPrimary}">${icon("content_copy", "text-[18px]")}<span>Kopyala</span></button><button type="button" data-cancel class="${UI.btnSecondary}">Kapat</button></div></div>`;
      $("[data-cancel]", m.el).onclick = () => m.close();
      $("#copy-key", m.el).onclick = async () => {
        try {
          await navigator.clipboard.writeText(secret);
          toast("Anahtar panoya kopyalandı. Yapıştırdıktan sonra panoyu temizleyin.");
        } catch {
          toast("Kopyalanamadı; anahtarı elle seçip kopyalayın.", "bad");
        }
      };
      timer = setInterval(() => {
        left -= 1;
        const ring = $("#ring", m.el);
        if (ring) ring.style.strokeDashoffset = String(((30 - left) / 30) * 100.5);
        const num = $("#ring-num", m.el);
        if (num) num.textContent = String(Math.max(left, 0));
        if (left <= 0) {
          m.close();
          toast("Anahtar ekrandan temizlendi.");
        }
      }, 1000);
    } catch (err) {
      $("#reveal-err", m.el).textContent = err.message;
      $("#reveal-pw", m.el).select();
    }
  };
}

function openStoreKeyModal(dev, onDone) {
  const m = openModal({
    title: `Anahtarı kaydet · ${deviceName(dev)}`,
    html: `<form id="store-form" class="space-y-4">
      <p class="font-body-md text-body-md text-on-surface-variant">Bilgisayarda <strong>Anahtar Yedekleme</strong> aracının gösterdiği 64 karakterlik anahtarı yapıştırın. Sunucuda şifreli saklanır; listede yalnızca parmak izi görünür.</p>
      <div><label class="${UI.label}">Anahtar</label><input id="store-secret" type="password" class="${UI.input} font-code-md text-code-md" autocomplete="off" spellcheck="false" placeholder="Anahtarı yapıştırın"/></div>
      <p id="store-err" class="text-error font-body-md text-body-md min-h-[20px]"></p>
      <div class="flex justify-end gap-2"><button type="button" data-cancel class="${UI.btnSecondary}">Vazgeç</button><button type="submit" class="${UI.btnPrimary}">Kaydet</button></div>
    </form>`,
  });
  $("[data-cancel]", m.el).onclick = () => m.close();
  $("#store-form", m.el).onsubmit = async (e) => {
    e.preventDefault();
    try {
      const r = await put(`/api/v1/admin/devices/${dev.device_id || dev.id}/secret`, { secret: $("#store-secret", m.el).value });
      m.close();
      toast(`Anahtar kaydedildi. Parmak izi ${r.fingerprint}. Kâğıttaki/parola yöneticisindeki kopyayla karşılaştırın.`);
      if (onDone) onDone();
    } catch (err) {
      $("#store-err", m.el).textContent = err.message;
    }
  };
}

function keyStatusPill(v) {
  if (!v.enabled) return pill("neutral", "Kasa kapalı");
  return v.has_key ? pill("ok", "Kayıtlı ✓") : pill("bad", "Kayıtlı değil ⚠");
}

function copyBlock(text) {
  return `<div class="relative"><pre class="p-3 rounded-lg bg-inverse-surface text-inverse-on-surface font-code-sm text-code-sm overflow-auto whitespace-pre-wrap">${esc(text)}</pre><button type="button" data-copy="${esc(text)}" class="absolute top-2 right-2 w-7 h-7 rounded-md bg-white/10 hover:bg-white/20 text-inverse-on-surface inline-flex items-center justify-center" title="Kopyala">${icon("content_copy", "text-[16px]")}</button></div>`;
}

function bindCopy(root) {
  $$("[data-copy]", root).forEach((b) => {
    b.onclick = async () => {
      try {
        await navigator.clipboard.writeText(b.getAttribute("data-copy"));
        toast("Kopyalandı.");
      } catch {
        toast("Kopyalanamadı.", "bad");
      }
    };
  });
}

function recoveryGuide(dev, generation) {
  const idShort = String(dev.device_id || dev.id).slice(0, 8);
  const dept = String(dev.department || "").trim() || "<departman>";
  const drivePath = `repositories/${dept}/${dev.hostname}--${idShort}/${generation || "<generation>"}`;
  const cmds = [
    "# Yeni bilgisayarda, yönetici PowerShell:",
    '$env:RESTIC_PASSWORD = "<kasadan gösterdiğiniz anahtar>"',
    `rclone copy "stowline-drive:${drivePath}" C:\\Recovery\\repo-copy`,
    "stowline-recovery.exe copy --src C:\\Recovery\\repo-copy --dst D:\\independent\\repo-copy",
    `stowline-recovery.exe verify --repo D:\\independent\\repo-copy --restic C:\\Stowline\\bin\\restic.exe --sha256 ${RESTIC_PIN}`,
    `stowline-recovery.exe list-snapshots --repo D:\\independent\\repo-copy --restic C:\\Stowline\\bin\\restic.exe --sha256 ${RESTIC_PIN}`,
    `stowline-recovery.exe restore-to-staging --repo D:\\independent\\repo-copy --restic C:\\Stowline\\bin\\restic.exe --sha256 ${RESTIC_PIN} --snapshot <snapshot-id> --staging D:\\Recovery\\staging\\case-1`,
  ].join("\n");
  const step = (n, title, body) => `<li class="flex gap-3"><span class="w-6 h-6 rounded-full bg-primary text-on-primary flex items-center justify-center font-label-md text-label-md shrink-0">${n}</span><div class="min-w-0 flex-1"><div class="font-label-md text-label-md font-bold text-on-surface">${title}</div><div class="mt-1 font-body-sm text-body-sm text-outline">${body}</div></div></li>`;
  return `<div class="${UI.card} p-5">
    <h3 class="font-headline-sm text-headline-sm font-bold text-on-surface flex items-center gap-2">${icon("local_fire_department", "text-amber-500")}Bilgisayar yandı ya da kayboldu mu?</h3>
    <ol class="mt-4 space-y-4">
      ${step(1, "Kasadan anahtarı göster", "Bu sayfadaki <strong>Göster</strong> düğmesi; yönetici şifresi gerekir.")}
      ${step(2, "Depodaki klasörü bul", `<span class="font-code-sm text-code-sm break-all text-on-surface">${esc(drivePath)}</span>`)}
      ${step(3, "Yeni bilgisayarda kurtarma aracını çalıştır", copyBlock(cmds))}
      ${step(4, "Dosyaları geri yükle", "Dosyalar yalnızca <strong>bekleme (staging)</strong> klasörüne yazılır; kontrol edip yerine kopyalayın.")}
    </ol>
    <p class="mt-4 font-body-sm text-body-sm text-outline">Anahtarın <strong>çevrimdışı bir kopyasını</strong> (parola yöneticisi + kasadaki kâğıt) ayrıca saklayın; bu kasa yalnızca ek bir kopyadır.</p></div>`;
}

function keyMetaRows(v) {
  const row = (k, val) => `<div class="flex justify-between gap-3 py-2 border-b border-outline-variant/60 last:border-b-0"><dt class="text-outline">${k}</dt><dd class="text-on-surface text-right">${val}</dd></div>`;
  return `<dl class="font-body-md text-body-md">${row("Durum", keyStatusPill(v))}${row("Parmak izi", `<span class="font-code-md text-code-md">${esc(v.fingerprint || "—")}</span>`)}${row("Kaynak", esc(SOURCE_TR[v.source] || "—"))}${row("Kaydedilme", esc(fmt(v.updated_at)))}${row("Son görüntüleme", v.last_revealed_at ? `${esc(fmt(v.last_revealed_at))} <span class="text-outline">(${v.reveal_count} kez)</span>` : "—")}</dl>`;
}

const VAULT_OFF = `<div class="mb-4 rounded-lg border border-amber-300 bg-amber-50 text-amber-900 px-4 py-3 font-body-md text-body-md">Şifre kasası sunucuda henüz yapılandırılmadı (<span class="font-code-sm text-code-sm">STOWLINE_ESCROW_KEY</span> tanımlı değil); anahtar kaydedilemez ve gösterilemez.</div>`;

function renderSifrelerTab(body, d, dv) {
  const v = d.vault || {};
  const gen = d.repository && d.repository.generation_id;
  body.innerHTML = `${v.enabled ? "" : VAULT_OFF}
    <div class="grid grid-cols-1 lg:grid-cols-12 gap-6">
      <div class="lg:col-span-5 ${UI.card} p-5">
        <h3 class="font-headline-sm text-headline-sm font-bold text-on-surface flex items-center gap-2">${icon("key", "text-primary")}Yedekleme anahtarı</h3>
        <p class="mt-1 font-body-sm text-body-sm text-outline">Bu bilgisayarın buluttaki şifreli yedeklerini açan tek anahtar.</p>
        <div class="mt-3">${keyMetaRows({ ...v, enabled: v.enabled })}</div>
        <div class="mt-4 flex flex-wrap gap-2">
          <button type="button" id="k-show" class="${UI.btnPrimary}" ${v.has_key && v.enabled ? "" : "disabled"}>${icon("visibility", "text-[18px]")}<span>Göster</span></button>
          <button type="button" id="k-store" class="${UI.btnSecondary}" ${v.enabled ? "" : "disabled"}>${icon("edit", "text-[18px]")}<span>${v.has_key ? "Güncelle" : "Anahtarı kaydet"}</span></button>
        </div>
        ${v.has_key ? "" : `<p class="mt-3 font-body-sm text-body-sm text-outline">Bilgisayarda “Anahtar Yedekleme” aracını çalıştırın; araç anahtarı sunucuya da kaydedebilir. Ya da anahtarı buraya elle yapıştırın.</p>`}
      </div>
      <div class="lg:col-span-7">${recoveryGuide(dv, gen)}</div>
    </div>
    <p class="mt-4 font-body-sm text-body-sm text-outline">Yönetici (panel) şifresi burada gösterilmez; Ayarlar → Yönetici hesabı'ndan değiştirilir.</p>`;
  const show = $("#k-show");
  if (show) show.onclick = () => openRevealModal(dv);
  $("#k-store").onclick = () => openStoreKeyModal(dv, rerender);
  bindCopy(body);
}

Pages["sifre-kasasi"] = {
  live: false,
  async render(view) {
    const vlt = await api("/api/v1/admin/vault");
    const items = vlt.items || [];
    const missing = items.filter((x) => !x.has_key && x.lifecycle === "ACTIVE").length;
    const pick = items.find((x) => x.has_key) || items[0];
    let generation = "";
    if (pick) {
      try {
        const det = await api(`/api/v1/admin/devices/${pick.device_id}`);
        generation = det.repository && det.repository.generation_id;
      } catch {
        /* the guide falls back to a placeholder */
      }
    }
    view.innerHTML = `
      <section class="flex flex-col sm:flex-row sm:items-center justify-between gap-4 ${UI.card} p-5">
        <div><h1 class="font-headline-lg text-headline-lg text-primary font-bold tracking-tight">Şifre Kasası</h1><p class="font-body-md text-body-md text-outline mt-0.5">Her bilgisayarın yedekleme anahtarı · ${items.filter((x) => x.has_key).length}/${items.length} kayıtlı</p></div>
      </section>
      ${vlt.enabled ? "" : VAULT_OFF}
      <div class="rounded-lg border border-amber-300 bg-amber-50 text-amber-900 px-4 py-3 font-body-md text-body-md flex items-start gap-2">${icon("warning", "text-[20px] mt-0.5")}<span>Bu anahtarlar yedeklerinizi açan tek şeydir. Bilgisayar yanar ya da kaybolursa yedekleri geri almak için gerekir.${missing ? ` <strong>${missing} bilgisayarın anahtarı henüz kayıtlı değil.</strong>` : ""}</span></div>
      <section class="grid grid-cols-1 lg:grid-cols-12 gap-6">
        <div class="lg:col-span-12 ${UI.card} overflow-x-auto">${
          items.length
            ? `<table class="w-full text-left border-collapse"><thead><tr class="bg-surface-container-low border-b border-outline-variant"><th class="${UI.th}">Bilgisayar</th><th class="${UI.th}">Anahtar durumu</th><th class="${UI.th}">Parmak izi</th><th class="${UI.th}">Kaydedilme</th><th class="${UI.th}">Son görüntüleme</th><th class="${UI.th}"></th></tr></thead><tbody class="divide-y divide-outline-variant/60">${items
                .map(
                  (x) => `<tr class="hover:bg-surface-container-low/70">
                    <td class="${UI.td}"><a href="#/device/${esc(x.device_id)}/sifreler" class="block"><div class="font-code-md text-code-md font-semibold text-on-surface whitespace-nowrap">${esc(deviceName(x))}</div><div class="text-[11px] text-outline">${deviceWasRenamed(x) ? esc(x.hostname) + " · " : ""}${esc(siteLabel(x.site_id))} · ${esc(deptOf(x))}${x.lifecycle !== "ACTIVE" ? " · arşiv" : ""}</div></a></td>
                    <td class="${UI.td}">${keyStatusPill({ enabled: vlt.enabled, has_key: x.has_key })}</td>
                    <td class="${UI.td} font-code-md text-code-md">${esc(x.fingerprint || "—")}</td>
                    <td class="${UI.td} font-code-sm text-code-sm">${esc(fmt(x.updated_at))}</td>
                    <td class="${UI.td} font-code-sm text-code-sm">${esc(fmt(x.last_revealed_at))}</td>
                    <td class="${UI.td} text-right whitespace-nowrap"><button type="button" data-show="${esc(x.device_id)}" class="${UI.btnSecondary}" ${x.has_key && vlt.enabled ? "" : "disabled"}>Göster</button> <button type="button" data-store="${esc(x.device_id)}" class="${UI.btnSecondary}" ${vlt.enabled ? "" : "disabled"}>${x.has_key ? "Güncelle" : "Kaydet"}</button></td></tr>`
                )
                .join("")}</tbody></table>`
            : `<div class="p-12 text-center text-outline">Henüz bilgisayar yok.</div>`
        }</div>
        <div class="lg:col-span-12">${pick ? recoveryGuide(pick, generation) : ""}</div>
      </section>
      <p class="font-body-sm text-body-sm text-outline">Yönetici (panel) şifresi burada gösterilmez; Ayarlar → Yönetici hesabı'ndan değiştirilir.</p>`;
    $$("[data-show]", view).forEach((b) => (b.onclick = () => openRevealModal(items.find((x) => x.device_id === b.getAttribute("data-show")))));
    $$("[data-store]", view).forEach((b) => (b.onclick = () => openStoreKeyModal(items.find((x) => x.device_id === b.getAttribute("data-store")), rerender)));
    bindCopy(view);
  },
};
