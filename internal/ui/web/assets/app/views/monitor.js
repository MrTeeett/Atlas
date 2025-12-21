import { api } from "../api.js";
import { el, svg } from "../dom.js";
import { fmtBytes, fmtPct, fmtRate, fmtUptime } from "../format.js";
import { t } from "../i18n.js";
import { state } from "../state.js";

export async function renderMonitor(root, initialPage) {
  const mon = {
    page: initialPage || "overview",
    stats: null,
    info: null,
    hist: [],
    maxPoints: 180, // ~6 minutes at 2s
    timerStats: null,
    timerProcs: null,
    procRows: [],
    procSelected: null,
    procSortKey: "rss",
    procSortDir: "desc",
    procQuery: "",
    autostart: null,
    autostartAt: 0,
    autostartLoading: false,
    autostartError: "",
    ctx: null,
  };

  const nav = el("aside", { class: "sm-nav" });
  const main = el("div", { class: "sm-main" });
  const layout = el("div", { class: "sm" }, nav, main);
  root.append(layout);

  function replaceMain(...nodes) {
    for (const child of Array.from(main.children)) {
      if (child && typeof child._cleanup === "function") {
        try { child._cleanup(); } catch {}
      }
    }
    main.replaceChildren(...nodes);
  }

  const navItems = [
    { id: "overview", titleKey: "monitor.navOverview" },
    { id: "apps", titleKey: "monitor.navApps" },
    { id: "history", titleKey: "monitor.navHistory" },
    { id: "processes", titleKey: "monitor.navProcesses" },
  ];
  if (state.view === "processes") navItems.push({ id: "autostart", titleKey: "monitor.navAutostart" });

  const navNodes = new Map();
  for (const it of navItems) {
    const n = el("div", { class: "item", tabindex: "0", onclick: () => setPage(it.id) }, t(it.titleKey));
    navNodes.set(it.id, n);
    nav.append(n);
  }

  function setPage(id) {
    mon.page = id;
    for (const it of navItems) navNodes.get(it.id)?.classList.toggle("active", it.id === id);
    if (id === "autostart") tickAutostart(true).catch(() => {});
    renderPage();
  }

  function closeCtx() {
    if (!mon.ctx) return;
    mon.ctx.remove();
    mon.ctx = null;
  }

  function showCtx(x, y, items) {
    closeCtx();
    const menu = el("div", { class: "cm" });
    for (const it of items) {
      if (it.sep) { menu.append(el("hr")); continue; }
      const btn = el("button", { type: "button", onclick: () => { closeCtx(); it.action(); } }, it.label);
      if (it.disabled) btn.disabled = true;
      menu.append(btn);
    }
    menu.style.left = `${Math.max(8, x)}px`;
    menu.style.top = `${Math.max(8, y)}px`;
    document.body.append(menu);
    mon.ctx = menu;
    const rect = menu.getBoundingClientRect();
    let left = x, top = y;
    if (left + rect.width > window.innerWidth - 8) left = window.innerWidth - rect.width - 8;
    if (top + rect.height > window.innerHeight - 8) top = window.innerHeight - rect.height - 8;
    if (left < 8) left = 8;
    if (top < 8) top = 8;
    menu.style.left = `${left}px`;
    menu.style.top = `${top}px`;
  }

  function donut(label, pct, value, sub, color) {
    const p = Math.max(0, Math.min(100, Number(pct) || 0));
    const r = 36;
    const c = 2 * Math.PI * r;
    const dash = (p / 100) * c;
    const svgNode = svg(
      `<svg viewBox="0 0 100 100" aria-hidden="true">
        <circle cx="50" cy="50" r="${r}" fill="none" stroke="rgba(255,255,255,.08)" stroke-width="10"></circle>
        <circle cx="50" cy="50" r="${r}" fill="none" stroke="${color}" stroke-width="10"
          stroke-linecap="round" transform="rotate(-90 50 50)"
          stroke-dasharray="${dash} ${c - dash}"></circle>
        <text x="50" y="56" text-anchor="middle" font-size="16" fill="rgba(255,255,255,.92)" font-weight="800">${p.toFixed(1)}%</text>
      </svg>`,
    );
    return el("div", { class: "gauge" },
      svgNode,
      el("div", {},
        el("div", { class: "title" }, label),
        el("div", { class: "val" }, value),
        el("div", { class: "sub" }, sub || " "),
      ),
    );
  }

  function chartCard(title, legend, drawFn) {
    const canvas = el("canvas");
    const wrap = el("div", { class: "chart" }, el("div", { class: "title" }, title), canvas);
    const leg = el("div", { class: "legend" });
    for (const it of legend) {
      leg.append(el("span", {}, el("span", { class: "dot", style: `background:${it.color}` }), it.label));
    }
    wrap.append(leg);

    function resize() {
      const rect = canvas.getBoundingClientRect();
      const dpr = window.devicePixelRatio || 1;
      const w = rect.width > 0 ? rect.width : 640;
      const h = rect.height > 0 ? rect.height : 180;
      canvas.width = Math.max(1, Math.floor(w * dpr));
      canvas.height = Math.max(1, Math.floor(h * dpr));
      const ctx = canvas.getContext("2d");
      if (!ctx) return;
      drawFn(ctx, dpr);
    }
    window.addEventListener("resize", resize);
    wrap._resize = resize;
    wrap._cleanup = () => window.removeEventListener("resize", resize);
    return wrap;
  }

  function drawChart(ctx, dpr, seriesList, opts) {
    const minY = Number(opts.minY ?? 0);
    const maxY = Number(opts.maxY ?? 100);
    const formatY = opts.formatY || ((v) => String(v));
    const xs = Array.isArray(opts.x) ? opts.x : null;
    const formatX = opts.formatX || ((v) => String(v));

    const w = ctx.canvas.width;
    const h = ctx.canvas.height;
    ctx.clearRect(0, 0, w, h);

    const padLeft = 58 * dpr;
    const padRight = 12 * dpr;
    const padTop = 12 * dpr;
    const padBottom = 30 * dpr;

    const innerW = Math.max(1, w - padLeft - padRight);
    const innerH = Math.max(1, h - padTop - padBottom);

    // Grid + Y labels
    ctx.strokeStyle = "rgba(255,255,255,.12)";
    ctx.lineWidth = 1 * dpr;
    ctx.fillStyle = "rgba(255,255,255,.78)";
    ctx.font = `${12 * dpr}px ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace`;
    ctx.textBaseline = "middle";
    ctx.textAlign = "right";

    const ticksY = Number(opts.ticksY ?? 5);
    for (let i = 0; i < ticksY; i++) {
      const t = ticksY <= 1 ? 0 : i / (ticksY - 1);
      const y = padTop + t * innerH;
      const value = maxY - t * (maxY - minY);
      ctx.beginPath();
      ctx.moveTo(padLeft, y);
      ctx.lineTo(w - padRight, y);
      ctx.stroke();
      ctx.fillText(formatY(value), padLeft - 8 * dpr, y);
    }

    // X grid + labels
    const len = Math.max(0, ...seriesList.map((s) => (s.data || []).length));
    if (len >= 2) {
      ctx.textBaseline = "top";
      ctx.textAlign = "center";
      const ticksX = Number(opts.ticksX ?? 5);
      for (let i = 0; i < ticksX; i++) {
        const t = ticksX <= 1 ? 0 : i / (ticksX - 1);
        const idx = Math.round(t * (len - 1));
        const x = padLeft + (idx / Math.max(1, len - 1)) * innerW;
        ctx.beginPath();
        ctx.moveTo(x, padTop);
        ctx.lineTo(x, padTop + innerH);
        ctx.stroke();
        const xVal = xs ? xs[idx] : idx;
        ctx.fillText(formatX(xVal), x, padTop + innerH + 6 * dpr);
      }
      ctx.textBaseline = "middle";
      ctx.textAlign = "right";
    }

    function toXY(i, v) {
      const x = padLeft + (i / Math.max(1, len - 1)) * innerW;
      const yNorm = (v - minY) / (maxY - minY || 1);
      const y = padTop + (1 - Math.max(0, Math.min(1, yNorm))) * innerH;
      return [x, y];
    }

    for (const s of seriesList) {
      const data = s.data || [];
      if (data.length < 2) continue;
      ctx.strokeStyle = s.color;
      ctx.lineWidth = 2 * dpr;
      ctx.beginPath();
      for (let i = 0; i < data.length; i++) {
        const [x, y] = toXY(i, Number(data[i]) || 0);
        if (i === 0) ctx.moveTo(x, y);
        else ctx.lineTo(x, y);
      }
      ctx.stroke();
    }
  }

  function niceCeil(v) {
    const x = Math.max(1, Number(v) || 0);
    const p = Math.pow(10, Math.floor(Math.log10(x)));
    const n = x / p;
    const m = n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10;
    return m * p;
  }

  async function tickStats() {
    mon.stats = await api("api/stats");
    mon.info = await api("api/system/info");
    mon.hist.push({
      t: Date.now(),
      cpu: mon.stats.cpu_usage_pct || 0,
      mem: mon.stats.mem_used_pct || 0,
      rx: mon.stats.net_rx_bytes_s || 0,
      tx: mon.stats.net_tx_bytes_s || 0,
    });
    if (mon.hist.length > mon.maxPoints) mon.hist.splice(0, mon.hist.length - mon.maxPoints);
    if (mon.page === "overview" || mon.page === "history") renderPage();
  }

  async function tickProcs() {
    const data = await api("api/processes");
    mon.procRows = data.processes || [];
    if (mon.page === "processes" || mon.page === "apps") renderPage();
  }

  async function tickAutostart(force = false) {
    if (mon.autostartLoading) return;
    const now = Date.now();
    if (!force && mon.autostartAt && (now - mon.autostartAt) < 15000) return;
    mon.autostartLoading = true;
    mon.autostartError = "";
    try {
      mon.autostart = await api("api/system/autostart");
    } catch (e) {
      mon.autostartError = String(e?.message || e || "error");
    } finally {
      mon.autostartAt = Date.now();
      mon.autostartLoading = false;
      if (mon.page === "processes") renderPage();
    }
  }

  function renderOverview() {
    const s = mon.stats;
    const i = mon.info;
    if (!s || !i) {
      replaceMain(el("div", { class: "path" }, t("common.loading")));
      return;
    }

    const grid = el("div", { class: "sm-grid" },
      donut(t("monitor.memory"), s.mem_used_pct, `${fmtBytes(s.mem_used_bytes)} / ${fmtBytes(s.mem_total_bytes)}`, t("monitor.used"), "#ff5c7a"),
      donut(t("monitor.disk"), s.disk_used_pct, `${fmtBytes(s.disk_used_bytes)} / ${fmtBytes(s.disk_total_bytes)}`, t("monitor.used"), "#ffb020"),
      donut(t("monitor.cpu"), s.cpu_usage_pct, fmtPct(s.cpu_usage_pct), s.cpu_cores ? t("monitor.cores", { n: s.cpu_cores }) : "", "#4f7cff"),
    );

    const net = el("div", { class: "chart" },
      el("div", { class: "title" }, t("monitor.network")),
      el("div", { class: "row" },
        kpi(t("monitor.download"), fmtRate(s.net_rx_bytes_s), "↓"),
        kpi(t("monitor.upload"), fmtRate(s.net_tx_bytes_s), "↑"),
      ),
    );

    const sys = el("div", { class: "chart" },
      el("div", { class: "title" }, t("monitor.system")),
      el("div", { class: "kv" },
        el("div", { class: "k" }, t("monitor.hostname")), el("div", {}, i.hostname || "—"),
        el("div", { class: "k" }, t("monitor.os")), el("div", {}, i.os || "—"),
        el("div", { class: "k" }, t("monitor.kernel")), el("div", {}, i.kernel || "—"),
        el("div", { class: "k" }, t("monitor.uptime")), el("div", {}, fmtUptime(i.uptime_seconds)),
        el("div", { class: "k" }, t("monitor.load")), el("div", {}, `${(i.load1 || 0).toFixed(2)} ${(i.load5 || 0).toFixed(2)} ${(i.load15 || 0).toFixed(2)}`),
      ),
    );

    replaceMain(grid, net, sys);
  }

  function renderHistory() {
    if (!mon.hist.length || !mon.stats) {
      replaceMain(el("div", { class: "path" }, t("common.loading")));
      return;
    }
    const h = mon.hist;
    const cpu = h.map(p => p.cpu);
    const mem = h.map(p => p.mem);
    const rx = h.map(p => p.rx);
    const tx = h.map(p => p.tx);
    const xt = h.map(p => p.t);

    const cpuNow = cpu[cpu.length - 1] || 0;
    const memNow = mem[mem.length - 1] || 0;
    const rxNow = rx[rx.length - 1] || 0;
    const txNow = tx[tx.length - 1] || 0;
    const netMax = niceCeil(Math.max(1, ...rx, ...tx));
    const lastT = xt[xt.length - 1] || Date.now();
    const fmtX = (tMs) => {
      const delta = Math.max(0, Number(lastT) - Number(tMs || 0));
      if (delta < 1500) return t("monitor.now");
      const sec = Math.round(delta / 1000);
      const m = Math.floor(sec / 60);
      const s = sec % 60;
      if (m > 0) return `-${m}:${String(s).padStart(2, "0")}`;
      return t("monitor.secondsAgo", { s });
    };

    const cpuCard = chartCard(
      t("monitor.historyCpuTitle", { now: fmtPct(cpuNow) }),
      [{ label: t("monitor.legendCpu", { now: fmtPct(cpuNow) }), color: "#4f7cff" }],
      (ctx, dpr) => drawChart(ctx, dpr, [{ data: cpu, color: "#4f7cff" }], { x: xt, formatX: fmtX, minY: 0, maxY: 100, formatY: (v) => `${Math.round(v)}%` }),
    );
    const memCard = chartCard(
      t("monitor.historyMemTitle", { now: fmtPct(memNow), used: fmtBytes(mon.stats.mem_used_bytes), total: fmtBytes(mon.stats.mem_total_bytes) }),
      [{ label: t("monitor.legendRam", { now: fmtPct(memNow) }), color: "#ff5c7a" }],
      (ctx, dpr) => drawChart(ctx, dpr, [{ data: mem, color: "#ff5c7a" }], { x: xt, formatX: fmtX, minY: 0, maxY: 100, formatY: (v) => `${Math.round(v)}%` }),
    );
    const netCard = chartCard(
      t("monitor.historyNetTitle", { rx: fmtRate(rxNow), tx: fmtRate(txNow) }),
      [{ label: t("monitor.legendRx", { val: fmtRate(rxNow) }), color: "#20c997" }, { label: t("monitor.legendTx", { val: fmtRate(txNow) }), color: "#ffb020" }],
      (ctx, dpr) => drawChart(ctx, dpr, [{ data: rx, color: "#20c997" }, { data: tx, color: "#ffb020" }], { x: xt, formatX: fmtX, minY: 0, maxY: netMax, formatY: (v) => fmtRate(v) }),
    );

    replaceMain(cpuCard, memCard, netCard);
    requestAnimationFrame(() => {
      cpuCard._resize?.();
      memCard._resize?.();
      netCard._resize?.();
    });
  }

  function procSortedFiltered() {
    const q = (mon.procQuery || "").toLowerCase().trim();
    let rows = mon.procRows.slice();
    if (q) rows = rows.filter(p => (p.command || "").toLowerCase().includes(q) || String(p.pid).includes(q) || (p.user || "").toLowerCase().includes(q));
    const dir = mon.procSortDir === "asc" ? 1 : -1;
    rows.sort((a, b) => {
      const key = mon.procSortKey;
      const va = key === "pid" ? a.pid
        : key === "cpu" ? (a.cpu_usage_pct || 0)
          : key === "rss" ? (a.rss_bytes || 0)
            : key === "user" ? (a.user || "")
              : (a.command || "");
      const vb = key === "pid" ? b.pid
        : key === "cpu" ? (b.cpu_usage_pct || 0)
          : key === "rss" ? (b.rss_bytes || 0)
            : key === "user" ? (b.user || "")
              : (b.command || "");
      if (typeof va === "string" || typeof vb === "string") return String(va).localeCompare(String(vb)) * dir;
      if (va === vb) return (a.pid - b.pid) * dir;
      return (va > vb ? 1 : -1) * dir;
    });
    return rows;
  }

  async function sendSignal(sig, pidOrPids) {
    if (!state.canProcs) return;
    const body = Array.isArray(pidOrPids) ? { pids: pidOrPids, signal: sig } : { pid: pidOrPids, signal: sig };
    await api("api/processes/signal", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(body),
    });
    await tickProcs();
  }

  function renderProcesses() {
    const head = el("div", { class: "pm-head" },
      el("div", { class: "pm-title" }, t("monitor.navProcesses")),
      el("span", { class: "pm-pill" }, `${t("monitor.processUser")}: ${state.me || "—"}`),
      el("span", { class: "pm-pill" }, state.canProcs ? t("monitor.processMgmtYes") : t("monitor.processMgmtNo")),
      el("span", { class: "pm-spacer" }),
      el("input", { class: "pm-search", placeholder: t("monitor.search"), value: mon.procQuery, oninput: (e) => { mon.procQuery = e.target.value || ""; renderPage(); } }),
      el("div", { class: "pm-actions" },
        el("button", { class: "secondary", onclick: () => tickProcs().catch(() => {}) }, t("monitor.update")),
        el("button", { class: "danger", onclick: () => sendSignal("TERM", mon.procSelected), disabled: !(state.canProcs && mon.procSelected) ? "disabled" : null }, t("monitor.terminate")),
      ),
    );

    function header(label, key, alignRight) {
      const th = el("th", { class: mon.procSortKey === key ? `sort ${mon.procSortDir}` : "", style: alignRight ? "text-align:right" : "" }, label);
      th.addEventListener("click", () => {
        if (mon.procSortKey === key) mon.procSortDir = mon.procSortDir === "asc" ? "desc" : "asc";
        else { mon.procSortKey = key; mon.procSortDir = key === "command" || key === "user" ? "asc" : "desc"; }
        renderPage();
      });
      return th;
    }

    const table = el("table", { class: "pm-table" },
      el("thead", {}, el("tr", {},
        header(t("monitor.thPID"), "pid", true),
        header(t("monitor.thCommand"), "command", false),
        header(t("monitor.thUser"), "user", false),
        header(t("monitor.thCPU"), "cpu", true),
        header(t("monitor.thRSS"), "rss", true),
        el("th", {}, t("monitor.thState")),
      )),
    );

    const tbody = el("tbody");
    const rows = procSortedFiltered();
    for (const p of rows) {
      const tr = el("tr", { class: "pm-row", "data-pid": p.pid });
      tr.append(
        el("td", { class: "mono", style: "text-align:right" }, p.pid),
        el("td", { class: "mono" }, p.command || ""),
        el("td", {}, p.user || "—"),
        el("td", { style: "text-align:right" }, fmtPct(p.cpu_usage_pct || 0)),
        el("td", { style: "text-align:right" }, fmtBytes(p.rss_bytes)),
        el("td", {}, p.state || ""),
      );
      tr.addEventListener("click", () => { mon.procSelected = p.pid; renderPage(); });
      tr.addEventListener("contextmenu", (e) => {
        e.preventDefault();
        mon.procSelected = p.pid;
        renderPage();
        const disabled = !state.canProcs;
        showCtx(e.clientX, e.clientY, [
          { label: t("monitor.sigTerm"), action: () => sendSignal("TERM", p.pid), disabled },
          { label: t("monitor.sigKill"), action: () => sendSignal("KILL", p.pid), disabled },
          { sep: true },
          { label: t("monitor.sigStop"), action: () => sendSignal("STOP", p.pid), disabled },
          { label: t("monitor.sigCont"), action: () => sendSignal("CONT", p.pid), disabled },
          { sep: true },
          { label: t("monitor.sigInt"), action: () => sendSignal("INT", p.pid), disabled },
          { label: t("monitor.sigHup"), action: () => sendSignal("HUP", p.pid), disabled },
          { label: t("monitor.sigUsr1"), action: () => sendSignal("USR1", p.pid), disabled },
          { label: t("monitor.sigUsr2"), action: () => sendSignal("USR2", p.pid), disabled },
        ]);
      });
      if (Number(mon.procSelected) === Number(p.pid)) tr.classList.add("selected");
      tbody.append(tr);
    }
    table.append(tbody);
    replaceMain(head, table);
  }

  function renderAutostart() {
    const head = el("div", { class: "pm-head" },
      el("div", { class: "pm-title" }, t("monitor.autostartTitle")),
      el("span", { class: "pm-spacer" }),
      el("button", { class: "secondary", onclick: () => tickAutostart(true).catch(() => {}) }, t("common.refresh")),
    );

    if (mon.autostartLoading && !mon.autostart) {
      replaceMain(head, el("div", { class: "path" }, t("common.loading")));
      return;
    }
    if (mon.autostartError) {
      replaceMain(head, el("div", { class: "path" }, mon.autostartError));
      return;
    }

    const as = mon.autostart;
    if (!as) {
      replaceMain(head, el("div", { class: "path" }, t("common.loading")));
      return;
    }
    if (!as.supported) {
      replaceMain(head, el("div", { class: "path" }, t("monitor.autostartUnsupported")), as.message ? el("div", { class: "path" }, as.message) : null);
      return;
    }

    const items = Array.isArray(as.items) ? as.items.slice() : [];
    items.sort((a, b) => String(a.unit || "").localeCompare(String(b.unit || "")));
    if (!items.length) {
      replaceMain(head, el("div", { class: "path" }, t("monitor.autostartEmpty")), as.message ? el("div", { class: "path" }, as.message) : null);
      return;
    }

    const table = el("table", { class: "pm-table" },
      el("thead", {}, el("tr", {},
        el("th", {}, t("monitor.autostartThUnit")),
        el("th", {}, t("monitor.autostartThDesc")),
        el("th", {}, t("monitor.autostartThState")),
      )),
    );
    const body = el("tbody");
    for (const it of items) {
      const st = `${it.active_state || "unknown"}${it.sub_state ? `/${it.sub_state}` : ""}`;
      body.append(el("tr", {},
        el("td", { class: "mono" }, it.unit || ""),
        el("td", {}, it.description || "—"),
        el("td", {}, el("span", { class: "pm-pill" }, st)),
      ));
    }
    table.append(body);
    replaceMain(head, table, as.message ? el("div", { class: "path" }, as.message) : null);
  }

  function renderApps() {
    const rows = mon.procRows || [];
    const m = new Map();
    const pidsByName = new Map();
    for (const p of rows) {
      const key = (p.command || "").split(" ")[0] || "";
      if (!key) continue;
      const prev = m.get(key) || { name: key, rss: 0, cpu: 0, count: 0 };
      prev.rss += Number(p.rss_bytes) || 0;
      prev.cpu += Number(p.cpu_usage_pct) || 0;
      prev.count += 1;
      m.set(key, prev);
      const arr = pidsByName.get(key) || [];
      arr.push(p.pid);
      pidsByName.set(key, arr);
    }
    const list = Array.from(m.values()).sort((a, b) => b.rss - a.rss).slice(0, 200);
    let selectedApp = null;

    const head = el("div", { class: "pm-head" },
      el("div", { class: "pm-title" }, t("monitor.navApps")),
      el("span", { class: "pm-pill" }, `${t("monitor.processUser")}: ${state.me || "—"}`),
      el("span", { class: "pm-spacer" }),
      el("button", {
        class: "danger",
        onclick: async () => {
          if (!state.canProcs || !selectedApp) return;
          const pids = (pidsByName.get(selectedApp) || []).filter(Boolean);
          const ok = confirm(t("monitor.closeAppConfirm", { app: selectedApp, n: pids.length }));
          if (!ok) return;
          await sendSignal("TERM", pids);
        },
        disabled: !state.canProcs ? "disabled" : null,
        id: "apps-close",
      }, t("monitor.closeApp")),
      el("button", { class: "secondary", onclick: () => tickProcs() }, t("monitor.update")),
    );

    const table = el("table", { class: "pm-table" },
      el("thead", {}, el("tr", {},
        el("th", {}, t("monitor.thName")),
        el("th", { style: "text-align:right" }, t("monitor.thCpuSum")),
        el("th", { style: "text-align:right" }, t("monitor.thMemSum")),
        el("th", { style: "text-align:right" }, t("monitor.thProcCount")),
      )),
    );
    const tbody = el("tbody");
    for (const a of list) {
      const tr = el("tr", { class: "pm-row" },
        el("td", { class: "mono" }, a.name),
        el("td", { style: "text-align:right" }, fmtPct(a.cpu, 1)),
        el("td", { style: "text-align:right" }, fmtBytes(a.rss)),
        el("td", { style: "text-align:right" }, a.count),
      );
      tr.addEventListener("click", () => {
        selectedApp = a.name;
        for (const row of table.querySelectorAll("tr.pm-row")) row.classList.remove("selected");
        tr.classList.add("selected");
        const btn = head.querySelector("#apps-close");
        if (btn) btn.disabled = !(state.canProcs && selectedApp);
      });
      tr.addEventListener("dblclick", () => {
        mon.procQuery = a.name;
        setPage("processes");
      });
      tr.addEventListener("contextmenu", (e) => {
        e.preventDefault();
        selectedApp = a.name;
        for (const row of table.querySelectorAll("tr.pm-row")) row.classList.remove("selected");
        tr.classList.add("selected");
        showCtx(e.clientX, e.clientY, [
          { label: t("monitor.showProcesses"), action: () => { mon.procQuery = a.name; setPage("processes"); } },
          { sep: true },
          {
            label: t("monitor.sigTerm"),
            disabled: !state.canProcs,
            action: async () => {
              const pids = (pidsByName.get(a.name) || []).filter(Boolean);
              const ok = confirm(t("monitor.termConfirm", { app: a.name, n: pids.length }));
              if (!ok) return;
              await sendSignal("TERM", pids);
            },
          },
          {
            label: t("monitor.sigKill"),
            disabled: !state.canProcs,
            action: async () => {
              const pids = (pidsByName.get(a.name) || []).filter(Boolean);
              const ok = confirm(t("monitor.killConfirm", { app: a.name, n: pids.length }));
              if (!ok) return;
              await sendSignal("KILL", pids);
            },
          },
        ]);
      });
      tbody.append(tr);
    }
    table.append(tbody);
    replaceMain(head, table);
  }

  function renderPage() {
    for (const it of navItems) navNodes.get(it.id)?.classList.toggle("active", it.id === mon.page);
    closeCtx();
    if (mon.page === "overview") renderOverview();
    else if (mon.page === "history") renderHistory();
    else if (mon.page === "apps") renderApps();
    else if (mon.page === "autostart") renderAutostart();
    else renderProcesses();
  }

  async function start() {
    setPage(mon.page);
    await tickStats();
    await tickProcs();
    if (mon.page === "autostart") await tickAutostart(true);
    mon.timerStats = setInterval(() => tickStats().catch(() => {}), 2000);
    mon.timerProcs = setInterval(() => tickProcs().catch(() => {}), 2000);
  }

  document.addEventListener("click", closeCtx);
  root._cleanup = () => {
    document.removeEventListener("click", closeCtx);
    closeCtx();
    if (mon.timerStats) clearInterval(mon.timerStats);
    if (mon.timerProcs) clearInterval(mon.timerProcs);
  };

  await start();
}

function kpi(label, value, sub) {
  return el("div", { class: "kpi" },
    el("div", { class: "label" }, label),
    el("div", { class: "value" }, value),
    el("div", { class: "sub" }, sub || " "),
  );
}
