const LS_KEY = "atlas.theme";

export function getTheme() {
  const v = (localStorage.getItem(LS_KEY) || "").trim().toLowerCase();
  if (v === "light" || v === "dark") return v;
  return "dark";
}

export function applyTheme(theme) {
  const t = theme === "light" ? "light" : "dark";
  document.documentElement.dataset.theme = t;
  try { localStorage.setItem(LS_KEY, t); } catch {}
}

export function initTheme() {
  applyTheme(getTheme());
}

