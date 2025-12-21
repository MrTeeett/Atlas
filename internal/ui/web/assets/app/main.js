import { ensureMe } from "./api.js";
import { el } from "./dom.js";
import { initLang, t } from "./i18n.js";
import { state, views } from "./state.js";
import { initTheme } from "./theme.js";
import { renderFiles } from "./views/files.js";
import { renderTerminal } from "./views/terminal.js";
import { renderFirewall } from "./views/firewall.js";
import { renderMonitor } from "./views/monitor.js";
import { renderAdmin } from "./views/admin.js";
import { renderSettings } from "./views/settings.js";

function setView(id) {
  state.view = id;
  for (const a of document.querySelectorAll(".tab")) a.classList.toggle("active", a.dataset.view === id);
  render();
}

async function render() {
  const viewRoot = document.getElementById("view");
  if (viewRoot._cleanup) { try { viewRoot._cleanup(); } catch {} }
  viewRoot.replaceChildren();

  const map = {
    dashboard: (root) => renderMonitor(root, "overview"),
    files: renderFiles,
    terminal: renderTerminal,
    processes: (root) => renderMonitor(root, "processes"),
    firewall: renderFirewall,
    settings: renderSettings,
    admin: renderAdmin,
  };
  await map[state.view](viewRoot);
}

async function main() {
  initTheme();
  initLang();
  await ensureMe(true);
  const tabs = document.getElementById("tabs");
  const logoutLink = document.getElementById("logoutLink");
  const enabledViews = views.filter(v =>
    (!v.requiresExec || state.canExec) &&
    (!v.requiresFW || state.canFW) &&
    (!v.requiresAdmin || state.isAdmin),
  );

  function renderTabs() {
    tabs.replaceChildren(...enabledViews.map(v =>
      el("a", { href: "#", class: "tab", "data-view": v.id, onclick: (e) => { e.preventDefault(); setView(v.id); } }, t(v.titleKey || v.title || v.id)),
    ));
    for (const a of document.querySelectorAll(".tab")) a.classList.toggle("active", a.dataset.view === state.view);
    if (logoutLink) logoutLink.textContent = t("common.logout");
  }

  const u = new URL(window.location.href);
  const requestedView = (u.searchParams.get("view") || "").trim();

  window.addEventListener("atlas:lang", () => {
    renderTabs();
    render().catch(() => {});
  });

  renderTabs();
  const first = enabledViews.some(v => v.id === "dashboard") ? "dashboard" : enabledViews[0]?.id || "dashboard";
  setView(requestedView && enabledViews.some(v => v.id === requestedView) ? requestedView : first);
}

main().catch(e => {
  document.body.replaceChildren(el("pre", { class: "terminal mono" }, e.stack || e.message || String(e)));
});
