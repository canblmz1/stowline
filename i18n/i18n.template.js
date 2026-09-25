/* Stowline UI language layer (generated from i18n/i18n.template.js and
 * i18n/tr-en.json by scripts/build-i18n.py -- edit those, not the copies).
 *
 * The UIs are written in Turkish. In English mode this script translates
 * what is rendered -- text nodes, placeholder/title/aria-label attributes,
 * alert/confirm/prompt and the page title -- so application code (which
 * compares its own Turkish labels) never changes. A text is looked up whole
 * first; otherwise every known phrase inside it is replaced, longest first
 * and on word boundaries only. Unknown text is left as it is.
 *
 * Language: saved choice ("stowline.lang" in localStorage), else the
 * browser / Windows UI language: Turkish -> Turkish, anything else -> English.
 */
(function () {
  "use strict";
  var DICT = /*__DICT__*/ {};
  var KEY = "stowline.lang";

  function detect() {
    try {
      var saved = window.localStorage.getItem(KEY);
      if (saved === "tr" || saved === "en") return saved;
    } catch (e) {}
    var nav = String((navigator.languages && navigator.languages[0]) || navigator.language || "").toLowerCase();
    return nav.indexOf("tr") === 0 ? "tr" : "en";
  }

  var lang = detect();
  window.stowlineLang = lang;
  document.documentElement.setAttribute("lang", lang);

  function toggle() {
    try {
      window.localStorage.setItem(KEY, lang === "tr" ? "en" : "tr");
    } catch (e) {}
    window.location.reload();
  }

  var FIXED_STYLE =
    "position:fixed;right:12px;bottom:12px;z-index:2147483000;font:600 12px system-ui,sans-serif;" +
    "padding:6px 10px;border-radius:999px;border:1px solid rgba(128,128,128,.4);" +
    "background:rgba(255,255,255,.9);color:#111;cursor:pointer;opacity:.75";
  var SLOT_STYLE =
    "font:600 11px system-ui,sans-serif;padding:3px 8px;border-radius:999px;border:1px solid currentColor;" +
    "background:transparent;color:inherit;cursor:pointer;opacity:.7;margin-left:8px;vertical-align:middle";

  function addSwitch() {
    var slot = document.getElementById("stowline-lang-slot");
    var have = document.getElementById("stowline-lang");
    if (have && (!slot || slot.contains(have))) return;
    if (have) have.remove();
    var b = document.createElement("button");
    b.id = "stowline-lang";
    b.type = "button";
    b.setAttribute("data-no-i18n", "");
    b.title = lang === "tr" ? "Switch to English" : "Türkçeye geç";
    b.textContent = lang === "tr" ? "EN" : "TR";
    b.style.cssText = slot ? SLOT_STYLE : FIXED_STYLE;
    b.onclick = toggle;
    (slot || document.body || document.documentElement).appendChild(b);
  }

  function keepSwitch() {
    addSwitch();
    new MutationObserver(addSwitch).observe(document.documentElement, { subtree: true, childList: true });
  }

  if (lang === "tr") {
    if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", keepSwitch);
    else keepSwitch();
    return;
  }

  // "re:<pattern>" keys reorder whole sentences with values inside them
  // (English word order differs); they run before phrase replacement.
  var patterns = [];
  var map = new Map();
  Object.keys(DICT).forEach(function (k) {
    if (k.indexOf("re:") === 0) {
      patterns.push([new RegExp(k.slice(3), "u"), DICT[k]]);
      return;
    }
    var t = k.trim();
    if (t && t.charAt(0) !== "_") map.set(t, DICT[k].trim());
  });
  var phrases = Array.from(map.keys())
    .filter(function (k) { return k.length >= 2; })
    .sort(function (a, b) { return b.length - a.length; });
  function esc(s) { return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"); }
  var phraseRe = new RegExp("(?<![\\p{L}\\p{N}])(" + phrases.map(esc).join("|") + ")(?![\\p{L}\\p{N}])", "gu");

  // Turkish writes the percent sign first ("%58"); English after ("58%").
  var pctRe = /(^|[\s(:\u00b7])%(\d+(?:[.,]\d+)?)(?=$|[\s).,])/g;

  function tr(s) {
    if (!s) return s;
    if (s.indexOf("%") !== -1) s = s.replace(pctRe, "$1$2%");
    var t = s.trim();
    if (!t || !/[A-Za-zÇĞİÖŞÜçğıöşü]/.test(t)) return s;
    if (map.has(t)) return s.replace(t, map.get(t));
    var out = t;
    for (var i = 0; i < patterns.length; i++) {
      if (patterns[i][0].test(out)) {
        out = out.replace(patterns[i][0], patterns[i][1]);
        break;
      }
    }
    return s.replace(t, out.replace(phraseRe, function (m) { return map.has(m) ? map.get(m) : m; }));
  }
  window.stowlineT = tr;

  var done = new WeakMap();
  var SKIP = { SCRIPT: 1, STYLE: 1, TEXTAREA: 1, CODE: 1, PRE: 0 };
  var ATTRS = ["placeholder", "title", "aria-label"];

  function skipped(el) {
    for (var n = el; n && n.nodeType === 1; n = n.parentNode) {
      if (SKIP[n.nodeName] === 1 || n.hasAttribute("data-no-i18n") || n.isContentEditable) return true;
    }
    return false;
  }

  function doText(node) {
    var v = node.nodeValue;
    if (done.get(node) === v) return;
    if (node.parentNode && skipped(node.parentNode)) return;
    var out = tr(v);
    done.set(node, out);
    if (out !== v) node.nodeValue = out;
  }

  function doAttrs(el) {
    if (skipped(el)) return;
    for (var i = 0; i < ATTRS.length; i++) {
      var a = ATTRS[i];
      if (el.hasAttribute(a)) {
        var v = el.getAttribute(a);
        var out = tr(v);
        if (out !== v) el.setAttribute(a, out);
      }
    }
    if (el.nodeName === "INPUT" && (el.type === "button" || el.type === "submit") && el.value) {
      var nv = tr(el.value);
      if (nv !== el.value) el.value = nv;
    }
  }

  function walk(root) {
    if (!root) return;
    if (root.nodeType === 3) return doText(root);
    if (root.nodeType !== 1 && root.nodeType !== 9 && root.nodeType !== 11) return;
    if (root.nodeType === 1) doAttrs(root);
    var tw = document.createTreeWalker(root, NodeFilter.SHOW_TEXT | NodeFilter.SHOW_ELEMENT);
    var n;
    while ((n = tw.nextNode())) {
      if (n.nodeType === 3) doText(n);
      else doAttrs(n);
    }
  }

  var origAlert = window.alert, origConfirm = window.confirm, origPrompt = window.prompt;
  window.alert = function (m) { return origAlert.call(window, tr(String(m))); };
  window.confirm = function (m) { return origConfirm.call(window, tr(String(m))); };
  window.prompt = function (m, d) { return origPrompt.call(window, tr(String(m)), d); };

  function start() {
    document.title = tr(document.title);
    walk(document.body);
    addSwitch();
    new MutationObserver(function (records) {
      addSwitch();
      for (var i = 0; i < records.length; i++) {
        var r = records[i];
        if (r.type === "characterData") doText(r.target);
        else if (r.type === "attributes") doAttrs(r.target);
        else for (var j = 0; j < r.addedNodes.length; j++) walk(r.addedNodes[j]);
      }
      var t = tr(document.title);
      if (t !== document.title) document.title = t;
    }).observe(document.documentElement, { subtree: true, childList: true, characterData: true, attributes: true, attributeFilter: ATTRS });
  }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", start);
  else start();
})();
