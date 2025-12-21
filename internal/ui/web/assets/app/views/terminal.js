import { api } from "../api.js";
import { el } from "../dom.js";
import { t } from "../i18n.js";
import { state } from "../state.js";
import { VTerm } from "../term/vt.js";

const LS_KEY = (user) => `atlas.term.tabs.${user || "anon"}`;

function b64FromBytes(bytes) {
  let s = "";
  for (let i = 0; i < bytes.length; i++) s += String.fromCharCode(bytes[i]);
  return btoa(s).replaceAll("=", "");
}

function tokenFromLine(line) {
  const s = (line || "").trimEnd();
  if (!s) return "";
  const parts = s.split(/\s+/);
  return parts[parts.length - 1] || "";
}

export async function renderTerminal(root) {
  const card = el("div", { class: "card term" });
  const bar = el("div", { class: "termbar" });
  const who = el("span", { class: "pill" }, `${t("terminal.web")}: ${state.me || "—"}`);

  const identities = await api("api/term/identities");
  const sel = el("select");
  for (const it of identities.identities || []) {
    sel.append(el("option", { value: it.id }, it.label || it.id));
  }
  const otherInput = el("input", { class: "mono", placeholder: t("terminal.linuxUserPlaceholder"), style: "min-width:180px; display:none;" });

  const tabsBar = el("div", { class: "term-tabs" });
  const btnNewTab = el("button", { class: "secondary" }, t("terminal.newTab"));
  const btnClear = el("button", { class: "secondary" }, t("terminal.clear"));
  const btnKill = el("button", { class: "danger" }, t("terminal.killTab"));
  bar.append(who, sel, otherInput, el("span", { class: "spacer" }), btnNewTab, btnClear, btnKill);

  const canvas = el("canvas");
  const suggest = el("div", { class: "suggest" });
  const ta = el("textarea", { class: "vterm-capture", autocapitalize: "off", autocomplete: "off", autocorrect: "off", spellcheck: "false" });
  const vscrollThumb = el("div", { class: "thumb" });
  const vscroll = el("div", { class: "vscroll hidden" }, vscrollThumb);
  const vterm = el("div", { class: "vterm" }, canvas, vscroll, suggest, ta);

  card.append(bar, tabsBar, vterm);
  root.append(card);

  const ctx = canvas.getContext("2d");
  let raf = null;
  let blinkOn = true;
  let blinkTimer = null;
  let suggestTimer = null;

  /** @type {{id:string, as:string, name:string, term:VTerm, streamAbort:AbortController|null, writeQueue:Uint8Array[], writeTimer:any, lineBuf:string, lastGrid:{cols:number, rows:number}, viewOffset:number, prevTotal:number, sel:null|{start:{x:number, line:number}, end:{x:number, line:number}, dragging:boolean}}[]} */
  let tabs = [];
  let active = 0;

  const suggestState = { visible: false, items: [], idx: 0, token: "" };

  function clamp(n, lo, hi) { return Math.max(lo, Math.min(hi, Number(n) || 0)); }

  function activeTab() { return tabs[active] || null; }

  function statusFromError(e) {
    const msg = (e && e.message) ? String(e.message) : String(e || "");
    const n = Number((msg.split(" ")[0] || "").trim());
    return Number.isFinite(n) ? n : 0;
  }

  function isMissingSessionError(e) {
    const s = statusFromError(e);
    return s === 404 || s === 410;
  }

  let lastMetrics = { dpr: 1, cellW: 8, cellH: 16, viewStartAbs: 0 };

  function maxOffsetFor(t) {
    if (!t) return 0;
    const total = t.term.getTotalLines();
    return Math.max(0, total - t.term.rows);
  }

  function viewStartFor(t) {
    const total = t.term.getTotalLines();
    const bottom = Math.max(0, total - t.term.rows);
    const off = clamp(t.viewOffset || 0, 0, Math.max(0, bottom));
    return Math.max(0, bottom - off);
  }

  function selectionIsEmpty(sel) {
    if (!sel) return true;
    return sel.start.line === sel.end.line && sel.start.x === sel.end.x;
  }

  function selectionNormalized(sel) {
    let a = { ...sel.start }, b = { ...sel.end };
    if (a.line > b.line || (a.line === b.line && a.x > b.x)) [a, b] = [b, a];
    return { a, b };
  }

  function selectionText(t) {
    if (!t?.sel || selectionIsEmpty(t.sel)) return "";
    const { a, b } = selectionNormalized(t.sel);
    const lines = [];
    for (let line = a.line; line <= b.line; line++) {
      const raw = t.term.getLineAbs(line) || "";
      let x0 = 0, x1 = t.term.cols;
      if (line === a.line) x0 = clamp(a.x, 0, t.term.cols);
      if (line === b.line) x1 = clamp(b.x, 0, t.term.cols);
      if (x1 < x0) [x0, x1] = [x1, x0];
      lines.push((raw.slice(x0, x1) || "").replace(/\s+$/g, ""));
    }
    return lines.join("\n");
  }

  async function copyToClipboard(text) {
    if (!text) return;
    try {
      await navigator.clipboard.writeText(text);
      return;
    } catch {}
    const tmp = el("textarea", { class: "vterm-capture" });
    tmp.value = text;
    document.body.append(tmp);
    tmp.select();
    try { document.execCommand("copy"); } catch {}
    tmp.remove();
  }

  function drawSelectionOverlay(t, cellW, cellH, viewStartAbs) {
    if (!t?.sel || selectionIsEmpty(t.sel)) return;
    const { a, b } = selectionNormalized(t.sel);
    const viewEnd = viewStartAbs + t.term.rows - 1;
    const from = Math.max(a.line, viewStartAbs);
    const to = Math.min(b.line, viewEnd);
    if (to < from) return;
    ctx.save();
    ctx.fillStyle = "rgba(79,124,255,.28)";
    for (let line = from; line <= to; line++) {
      const y = line - viewStartAbs;
      let x0 = 0, x1 = t.term.cols;
      if (line === a.line) x0 = clamp(a.x, 0, t.term.cols);
      if (line === b.line) x1 = clamp(b.x, 0, t.term.cols);
      if (x1 < x0) [x0, x1] = [x1, x0];
      ctx.fillRect(x0 * cellW, y * cellH, Math.max(1, (x1 - x0) * cellW), cellH);
    }
    ctx.restore();
  }

  function updateScrollbar(t) {
    if (!t) return;
    if (t.term.altActive) { vscroll.classList.add("hidden"); return; }
    const total = t.term.getTotalLines();
    const visible = t.term.rows;
    const maxScroll = Math.max(0, total - visible);
    if (maxScroll <= 0) { vscroll.classList.add("hidden"); return; }
    vscroll.classList.remove("hidden");

    const trackH = Math.max(1, vscroll.clientHeight);
    const minThumb = 28;
    const thumbH = Math.max(minThumb, Math.floor(trackH * (visible / total)));
    vscrollThumb.style.height = `${thumbH}px`;

    const viewStartAbs = viewStartFor(t);
    const maxTop = Math.max(0, trackH - thumbH);
    const top = maxTop * (viewStartAbs / Math.max(1, maxScroll));
    vscrollThumb.style.top = `${Math.round(top)}px`;
  }

  function scheduleRender() {
    if (!ctx) return;
    if (raf) return;
    raf = requestAnimationFrame(() => {
      raf = null;
      const t = activeTab();
      if (!t) return;
      const dpr = window.devicePixelRatio || 1;
      const rect = canvas.getBoundingClientRect();
      const w = Math.max(1, Math.floor(rect.width * dpr));
      const h = Math.max(1, Math.floor(rect.height * dpr));
      if (canvas.width !== w || canvas.height !== h) {
        canvas.width = w;
        canvas.height = h;
      }

      // Measure cell size
      ctx.font = `${Math.floor(14 * dpr)}px ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace`;
      const metrics = ctx.measureText("M");
      const cellW = Math.max(6 * dpr, Math.ceil(metrics.width));
      const cellH = Math.max(12 * dpr, Math.ceil(16 * dpr));

      const viewStartAbs = viewStartFor(t);
      lastMetrics = { dpr, cellW, cellH, viewStartAbs };
      t.term.render(ctx, cellW, cellH, blinkOn, { viewStartAbs, showCursor: t.viewOffset === 0 });
      drawSelectionOverlay(t, cellW, cellH, viewStartAbs);
      updateScrollbar(t);
      if (suggestState.visible) positionSuggest();
    });
  }

  function startBlink() {
    if (blinkTimer) return;
    blinkTimer = setInterval(() => {
      blinkOn = !blinkOn;
      scheduleRender();
    }, 500);
  }

  function stopBlink() {
    if (blinkTimer) clearInterval(blinkTimer);
    blinkTimer = null;
  }

  function setSuggestVisible(v) {
    suggestState.visible = v;
    suggest.classList.toggle("visible", v);
    if (!v) {
      suggestState.items = [];
      suggestState.idx = 0;
      suggestState.token = "";
      suggest.replaceChildren();
      suggest.style.left = "";
      suggest.style.top = "";
      suggest.style.maxHeight = "";
    }
  }

  function positionSuggest() {
    const t = activeTab();
    if (!t || !suggestState.visible) return;
    if (t.term.altActive || (t.viewOffset || 0) !== 0) return;

    const vrect = vterm.getBoundingClientRect();
    const crect = canvas.getBoundingClientRect();
    const insetX = crect.left - vrect.left;
    const insetY = crect.top - vrect.top;

    const cellWcss = lastMetrics.cellW / (lastMetrics.dpr || 1);
    const cellHcss = lastMetrics.cellH / (lastMetrics.dpr || 1);

    const tokenLen = (suggestState.token || "").length;
    const anchorX = clamp((t.term.cursorX || 0) - tokenLen, 0, t.term.cols - 1);
    const anchorY = clamp((t.term.cursorY || 0), 0, t.term.rows - 1);

    // First pass: set maxHeight based on available space.
    const aboveSpace = insetY + anchorY * cellHcss - 12;
    const belowTop = insetY + (anchorY + 1) * cellHcss + 8;
    const belowSpace = vrect.height - belowTop - 12;
    const preferAbove = aboveSpace >= 140 || aboveSpace >= belowSpace;

    const maxH = Math.max(120, Math.floor(preferAbove ? aboveSpace : belowSpace));
    suggest.style.maxHeight = `${Math.min(420, maxH)}px`;

    // Now measure and place.
    const srect = suggest.getBoundingClientRect();
    let left = insetX + anchorX * cellWcss;
    let top = preferAbove ? (insetY + anchorY * cellHcss - srect.height - 8) : belowTop;

    const minLeft = 10;
    const minTop = 10;
    const maxLeft = vrect.width - srect.width - 10;
    const maxTop = vrect.height - srect.height - 10;
    left = clamp(left, minLeft, Math.max(minLeft, maxLeft));
    top = clamp(top, minTop, Math.max(minTop, maxTop));

    // If we ended up overlapping the cursor line, force above if possible.
    const cursorTop = insetY + anchorY * cellHcss;
    const cursorBottom = cursorTop + cellHcss;
    const overlapsCursorLine = top < cursorBottom && (top + srect.height) > cursorTop;
    if (overlapsCursorLine && aboveSpace >= 120) {
      top = clamp(insetY + anchorY * cellHcss - srect.height - 8, minTop, Math.max(minTop, maxTop));
    }

    suggest.style.left = `${Math.round(left)}px`;
    suggest.style.top = `${Math.round(top)}px`;
  }

  function renderSuggest() {
    if (!suggestState.visible) return;
    suggest.replaceChildren(...suggestState.items.map((it, i) => {
      const row = el("div", {
        class: `item ${i === suggestState.idx ? "active" : ""}`,
        onclick: () => { suggestState.idx = i; acceptSuggest(); },
      },
      el("div", { class: "name" }, it.label),
      el("div", { class: "detail" }, it.detail || ""),
      );
      return row;
    }));
    requestAnimationFrame(positionSuggest);
  }

  async function flushWrite(t) {
    const parts = t.writeQueue;
    t.writeQueue = [];
    t.writeTimer = null;
    if (!parts.length) return;
    let total = 0;
    for (const p of parts) total += p.length;
    const all = new Uint8Array(total);
    let off = 0;
    for (const p of parts) { all.set(p, off); off += p.length; }
    const data_b64 = b64FromBytes(all);
    try {
      await api(`api/term/session/${encodeURIComponent(t.id)}/write`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ data_b64 }),
      });
    } catch (e) {
      if (!isMissingSessionError(e)) throw e;
      await reviveTab(t);
      await api(`api/term/session/${encodeURIComponent(t.id)}/write`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ data_b64 }),
      });
    }
  }

  function queueBytes(t, bytes) {
    if (!t || !bytes || !bytes.length) return;
    t.writeQueue.push(bytes);
    if (t.writeTimer) return;
    t.writeTimer = setTimeout(() => flushWrite(t).catch(() => {}), 20);
  }

  function calcGrid() {
    const probe = el("span", { class: "mono", style: "position:absolute; visibility:hidden; left:0; top:0;" }, "M");
    vterm.append(probe);
    const rect = probe.getBoundingClientRect();
    probe.remove();
    const cw = Math.max(7, rect.width || 8);
    const ch = Math.max(14, rect.height || 16);
    const cols = Math.max(20, Math.floor((canvas.clientWidth || 0) / cw));
    const rows = Math.max(8, Math.floor((canvas.clientHeight || 0) / ch));
    return { cols, rows };
  }

  async function sendResize(t) {
    if (!t) return;
    const g = calcGrid();
    if (g.cols === t.lastGrid.cols && g.rows === t.lastGrid.rows) return;
    t.lastGrid = g;
    t.term.resize(g.cols, g.rows);
    t.viewOffset = clamp(t.viewOffset || 0, 0, maxOffsetFor(t));
    scheduleRender();
    try {
      await api(`api/term/session/${encodeURIComponent(t.id)}/resize`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify(g),
      });
    } catch (e) {
      if (!isMissingSessionError(e)) throw e;
      // Avoid deadlock if we are already inside reviveTab().
      if (t._revivePromise) return;
      await reviveTab(t);
    }
  }

  const ro = new ResizeObserver(() => {
    const t = activeTab();
    sendResize(t).catch(() => {});
  });
  ro.observe(vterm);

  async function connectStream(t) {
    if (t.streamAbort) t.streamAbort.abort();
    t.streamAbort = new AbortController();

    const res = await fetch(`api/term/session/${encodeURIComponent(t.id)}/stream`, { signal: t.streamAbort.signal, credentials: "same-origin" });
    if (!res.ok) {
      if (res.status === 404 || res.status === 410) {
        // Avoid deadlock if we are already inside reviveTab().
        if (t._revivePromise) return;
        await reviveTab(t);
        return;
      }
      throw new Error(`${res.status} ${res.statusText}`);
    }
    if (!res.body) throw new Error("no stream body");

    const reader = res.body.getReader();
    const dec = new TextDecoder("utf-8", { fatal: false });
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      if (!value || !value.length) continue;
      const before = t.term.getTotalLines();
      t.term.write(dec.decode(value, { stream: true }));
      const after = t.term.getTotalLines();
      const delta = after - before;
      if (delta > 0 && (t.viewOffset || 0) > 0) {
        t.viewOffset = clamp((t.viewOffset || 0) + delta, 0, maxOffsetFor(t));
      }
      if (t === activeTab()) scheduleRender();
    }
  }

  function tabLabel(t, idx) {
    const suffix = t.id ? t.id.slice(0, 4) : String(idx + 1);
    return `${t.as} · ${suffix}`;
  }

  function persistTabs() {
    const data = tabs.map((t) => ({ id: t.id, as: t.as, name: t.name || "", view_offset: t.viewOffset || 0 }));
    localStorage.setItem(LS_KEY(state.me), JSON.stringify({ active, tabs: data }));
  }

  function renderTabs() {
    tabsBar.replaceChildren(...tabs.map((t, i) => {
      const x = el("span", { class: "x", onclick: (e) => { e.stopPropagation(); closeTab(i).catch(() => {}); } }, "×");
      const n = el("div", { class: `term-tab ${i === active ? "active" : ""}`, onclick: () => setActive(i) }, t.name || tabLabel(t, i), x);
      return n;
    }));
  }

  function setActive(i) {
    active = clamp(i, 0, Math.max(0, tabs.length - 1));
    renderTabs();
    setSuggestVisible(false);
    scheduleRender();
    const t = activeTab();
    if (t) sendResize(t).catch(() => {});
    persistTabs();
    ta.focus();
  }

  async function reviveTab(t) {
    if (!t) return;
    if (t._revivePromise) return t._revivePromise;
    t._revivePromise = (async () => {
      if (t.streamAbort) t.streamAbort.abort();

      const g = calcGrid();
      const created = await api("api/term/session", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ as: t.as || "self", cols: g.cols, rows: g.rows }),
      });

      t.id = created.id;
      t.as = created.as || t.as || "self";
      t.term = new VTerm(g.cols, g.rows, (s) => queueBytes(t, new TextEncoder().encode(s)));
      t.lineBuf = "";
      t.lastGrid = g;
      t.viewOffset = 0;
      t.prevTotal = 0;
      t.sel = null;
      t.writeQueue = [];
      if (t.writeTimer) clearTimeout(t.writeTimer);
      t.writeTimer = null;

      renderTabs();
      persistTabs();
      startBlink();
      connectStream(t).catch(() => {});
      scheduleRender();
      if (t === activeTab()) ta.focus();
      sendResize(t).catch(() => {});
    })().finally(() => {
      t._revivePromise = null;
    });
    return t._revivePromise;
  }

  async function createTab(as) {
    const g = calcGrid();
    const created = await api("api/term/session", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ as, cols: g.cols, rows: g.rows }),
    });

	    const t = {
	      id: created.id,
	      as: created.as || as || "self",
	      name: "",
	      term: null,
	      streamAbort: null,
	      _revivePromise: null,
	      writeQueue: [],
	      writeTimer: null,
	      lineBuf: "",
	      lastGrid: g,
	      viewOffset: 0,
      prevTotal: 0,
      sel: null,
    };
    t.term = new VTerm(g.cols, g.rows, (s) => queueBytes(t, new TextEncoder().encode(s)));

    tabs.push(t);
    active = tabs.length - 1;
    renderTabs();
    persistTabs();
    startBlink();
    connectStream(t).catch(() => {});
    scheduleRender();
    ta.focus();
  }

  async function closeTab(i) {
    const t = tabs[i];
    if (!t) return;
    if (t.streamAbort) t.streamAbort.abort();
    try { await api(`api/term/session/${encodeURIComponent(t.id)}`, { method: "DELETE" }); } catch {}
    if (t.writeTimer) clearTimeout(t.writeTimer);
    tabs.splice(i, 1);
    if (!tabs.length) {
      active = 0;
      stopBlink();
      renderTabs();
      persistTabs();
      scheduleRender();
      return;
    }
    if (active >= tabs.length) active = tabs.length - 1;
    renderTabs();
    persistTabs();
    scheduleRender();
  }

  async function updateSuggest() {
    const t = activeTab();
    if (!t || t.term.altActive) { setSuggestVisible(false); return; }
    const token = tokenFromLine(t.lineBuf);
    if (!token || token.length < 2 || token.length > 64 || !/^[a-zA-Z0-9._-]+$/.test(token)) { setSuggestVisible(false); return; }
    suggestState.token = token;
    const resp = await api(`api/term/complete?q=${encodeURIComponent(token)}`);
    const items = (resp.items || []).filter((x) => x?.label);
    if (!items.length) { setSuggestVisible(false); return; }
    suggestState.items = items;
    suggestState.idx = 0;
    setSuggestVisible(true);
    renderSuggest();
  }

  function scheduleSuggest() {
    if (suggestTimer) clearTimeout(suggestTimer);
    suggestTimer = setTimeout(() => updateSuggest().catch(() => setSuggestVisible(false)), 140);
  }

  function acceptSuggest() {
    const t = activeTab();
    if (!t) return;
    if (!suggestState.visible || !suggestState.items.length) return;
    const it = suggestState.items[suggestState.idx];
    const tok = suggestState.token;
    if (!it?.label || !tok) return;
    if (!it.label.startsWith(tok)) return;
    const rest = it.label.slice(tok.length);
    if (rest) queueBytes(t, new TextEncoder().encode(rest));
    queueBytes(t, new TextEncoder().encode(" "));
    t.lineBuf += rest + " ";
    setSuggestVisible(false);
    ta.focus();
  }

  canvas.addEventListener("mousedown", (e) => {
    const t = activeTab();
    if (!t) return;
    if (e.button !== 0) return;
    ta.focus();
    setSuggestVisible(false);
    const rect = canvas.getBoundingClientRect();
    const cellWcss = lastMetrics.cellW / (lastMetrics.dpr || 1);
    const cellHcss = lastMetrics.cellH / (lastMetrics.dpr || 1);
    const x = clamp(Math.floor((e.clientX - rect.left) / cellWcss), 0, t.term.cols - 1);
    const y = clamp(Math.floor((e.clientY - rect.top) / cellHcss), 0, t.term.rows - 1);
    const line = lastMetrics.viewStartAbs + y;
    t.sel = { start: { x, line }, end: { x, line }, dragging: true };
    scheduleRender();
    e.preventDefault();
  });
  window.addEventListener("mousemove", (e) => {
    const t = activeTab();
    if (!t?.sel?.dragging) return;
    const rect = canvas.getBoundingClientRect();
    const cellWcss = lastMetrics.cellW / (lastMetrics.dpr || 1);
    const cellHcss = lastMetrics.cellH / (lastMetrics.dpr || 1);
    const x = clamp(Math.floor((e.clientX - rect.left) / cellWcss), 0, t.term.cols - 1);
    const y = clamp(Math.floor((e.clientY - rect.top) / cellHcss), 0, t.term.rows - 1);
    const line = lastMetrics.viewStartAbs + y;
    t.sel.end = { x, line };
    scheduleRender();
  });
  window.addEventListener("mouseup", () => {
    const t = activeTab();
    if (!t?.sel) return;
    t.sel.dragging = false;
  });

  canvas.addEventListener("wheel", (e) => {
    const t = activeTab();
    if (!t) return;
    if (t.term.altActive) return; // don't break TUI apps using alt screen
    const max = maxOffsetFor(t);
    if (max <= 0) return;
    const lines = Math.max(1, Math.ceil(Math.abs(e.deltaY) / 30));
    if (e.deltaY < 0) t.viewOffset = clamp((t.viewOffset || 0) + lines, 0, max);
    else t.viewOffset = clamp((t.viewOffset || 0) - lines, 0, max);
    persistTabs();
    scheduleRender();
    e.preventDefault();
  }, { passive: false });

  let scrollDrag = null;
  vscrollThumb.addEventListener("mousedown", (e) => {
    const t = activeTab();
    if (!t) return;
    if (t.term.altActive) return;
    const total = t.term.getTotalLines();
    const visible = t.term.rows;
    const maxScroll = Math.max(0, total - visible);
    if (maxScroll <= 0) return;
    const trackH = Math.max(1, vscroll.clientHeight);
    const thumbH = Math.max(1, vscrollThumb.getBoundingClientRect().height);
    const maxTop = Math.max(0, trackH - thumbH);
    const startTop = parseFloat(getComputedStyle(vscrollThumb).top) || 0;
    scrollDrag = { startY: e.clientY, startTop, maxTop, maxScroll, visible, total };
    e.preventDefault();
    e.stopPropagation();
  });

  vscroll.addEventListener("mousedown", (e) => {
    if (e.target === vscrollThumb) return;
    const t = activeTab();
    if (!t) return;
    if (t.term.altActive) return;
    const total = t.term.getTotalLines();
    const visible = t.term.rows;
    const maxScroll = Math.max(0, total - visible);
    if (maxScroll <= 0) return;
    const trackRect = vscroll.getBoundingClientRect();
    const thumbRect = vscrollThumb.getBoundingClientRect();
    const trackH = Math.max(1, vscroll.clientHeight);
    const maxTop = Math.max(0, trackH - thumbRect.height);
    const clickY = e.clientY - trackRect.top;
    const targetTop = clamp(clickY - thumbRect.height / 2, 0, maxTop);
    const viewStartAbs = Math.round((targetTop / Math.max(1, maxTop)) * maxScroll);
    const bottom = Math.max(0, total - visible);
    t.viewOffset = clamp(bottom - viewStartAbs, 0, maxOffsetFor(t));
    persistTabs();
    scheduleRender();
    e.preventDefault();
  });

  window.addEventListener("mousemove", (e) => {
    if (!scrollDrag) return;
    const t = activeTab();
    if (!t) return;
    const dy = e.clientY - scrollDrag.startY;
    const top = clamp(scrollDrag.startTop + dy, 0, scrollDrag.maxTop);
    const viewStartAbs = Math.round((top / Math.max(1, scrollDrag.maxTop)) * scrollDrag.maxScroll);
    const bottom = Math.max(0, scrollDrag.total - scrollDrag.visible);
    t.viewOffset = clamp(bottom - viewStartAbs, 0, maxOffsetFor(t));
    persistTabs();
    scheduleRender();
  });

  window.addEventListener("mouseup", () => { scrollDrag = null; });

  canvas.addEventListener("contextmenu", (e) => {
    const t = activeTab();
    if (!t) return;
    if (t.sel && !selectionIsEmpty(t.sel)) {
      e.preventDefault();
      copyToClipboard(selectionText(t)).catch(() => {});
    }
  });

  ta.addEventListener("blur", () => setSuggestVisible(false));

  ta.addEventListener("keydown", (e) => {
    const t = activeTab();
    if (!t) return;

    if ((e.ctrlKey || e.metaKey) && (e.key === "c" || e.key === "C") && t.sel && !selectionIsEmpty(t.sel)) {
      e.preventDefault();
      copyToClipboard(selectionText(t)).catch(() => {});
      return;
    }
    if (e.key === "Escape" && t.sel && !t.sel.dragging) {
      t.sel = null;
      scheduleRender();
      // continue: also forward ESC to the app.
    }

    if (suggestState.visible) {
      if (e.key === "ArrowDown") { e.preventDefault(); suggestState.idx = Math.min(suggestState.items.length - 1, suggestState.idx + 1); renderSuggest(); return; }
      if (e.key === "ArrowUp") { e.preventDefault(); suggestState.idx = Math.max(0, suggestState.idx - 1); renderSuggest(); return; }
      if (e.key === "Escape") { e.preventDefault(); setSuggestVisible(false); return; }
      if (e.key === "Enter") { e.preventDefault(); acceptSuggest(); return; }
      if (e.key === "Tab") { e.preventDefault(); acceptSuggest(); return; }
    }

    const bytes = t.term.keyBytes(e);
    if (!bytes) return;
    e.preventDefault();

    if (t.sel && !t.sel.dragging && !e.ctrlKey && !e.altKey && e.key.length === 1) {
      t.sel = null;
      scheduleRender();
    }

    if (!t.term.altActive) {
      if (!e.ctrlKey && !e.altKey && e.key.length === 1) { t.lineBuf += e.key; scheduleSuggest(); }
      else if (e.key === "Backspace") { t.lineBuf = t.lineBuf.slice(0, -1); scheduleSuggest(); }
      else if (e.key === "Enter") { t.lineBuf = ""; setSuggestVisible(false); }
      else if (e.key === "ArrowUp" || e.key === "ArrowDown") { setSuggestVisible(false); }
    }

    queueBytes(t, bytes);
  });

  ta.addEventListener("paste", (e) => {
    const t = activeTab();
    if (!t) return;
    const text = e.clipboardData?.getData("text/plain") || "";
    if (!text) return;
    e.preventDefault();
    const bytes = t.term.pasteBytes(text);
    if (bytes) queueBytes(t, bytes);
  });

  btnClear.onclick = () => {
    const t = activeTab();
    if (!t) return;
    t.term.reset();
    t.viewOffset = 0;
    t.sel = null;
    scheduleRender();
    setSuggestVisible(false);
    ta.focus();
  };
  btnKill.onclick = () => closeTab(active).catch(() => {});
  btnNewTab.onclick = async () => {
    const as = sel.value || "self";
    await createTab(as === "__other" ? (otherInput.value || "self") : as);
  };

  sel.addEventListener("change", async () => {
    setSuggestVisible(false);
    const as = sel.value || "self";
    if (as === "__other") {
      otherInput.style.display = "";
      otherInput.focus();
      return;
    }
    otherInput.style.display = "none";
    await createTab(as);
  });

  if (identities.allow_any) {
    sel.append(el("option", { value: "__other" }, t("terminal.other")));
  }

  otherInput.addEventListener("keydown", async (e) => {
    if (e.key !== "Enter") return;
    e.preventDefault();
    const as = (otherInput.value || "").trim();
    if (!as) return;
    await createTab(as);
    otherInput.style.display = "none";
    sel.value = "__other";
  });

  // Restore tabs
  let restored = false;
  try {
    const raw = localStorage.getItem(LS_KEY(state.me));
    const saved = raw ? JSON.parse(raw) : null;
    if (saved?.tabs?.length) {
      active = clamp(saved.active || 0, 0, saved.tabs.length - 1);
      for (const st of saved.tabs) {
        const g = calcGrid();
	        const t = {
	          id: String(st.id || ""),
	          as: String(st.as || "self"),
	          name: String(st.name || ""),
	          term: null,
	          streamAbort: null,
	          _revivePromise: null,
	          writeQueue: [],
	          writeTimer: null,
	          lineBuf: "",
	          lastGrid: g,
	          viewOffset: Number(st.view_offset) || 0,
          prevTotal: 0,
          sel: null,
        };
        t.term = new VTerm(g.cols, g.rows, (s) => queueBytes(t, new TextEncoder().encode(s)));
        tabs.push(t);
        connectStream(t).catch(() => {});
      }
      restored = tabs.length > 0;
    }
  } catch {}

  if (!restored) {
    await createTab("self");
  } else {
    renderTabs();
    persistTabs();
    startBlink();
    scheduleRender();
    const t = activeTab();
    if (t) sendResize(t).catch(() => {});
    ta.focus();
  }

  const onWinResize = () => {
    if (suggestState.visible) requestAnimationFrame(positionSuggest);
  };
  window.addEventListener("resize", onWinResize);

  root._cleanup = () => {
    ro.disconnect();
    stopBlink();
    if (raf) cancelAnimationFrame(raf);
    raf = null;
    if (suggestTimer) clearTimeout(suggestTimer);
    window.removeEventListener("resize", onWinResize);
    for (const t of tabs) {
      if (t.streamAbort) t.streamAbort.abort();
      if (t.writeTimer) clearTimeout(t.writeTimer);
    }
  };
}
