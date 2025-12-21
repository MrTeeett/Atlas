import { state } from "./state.js";

let mePromise = null;

export async function ensureMe(force = false) {
  if (!force && state.csrf) return;
  if (!force && mePromise) return mePromise;
  mePromise = (async () => {
    const res = await fetch("api/me");
    if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
    const me = await res.json();
    state.csrf = me.csrf || "";
    state.me = me.user || "";
    state.role = me.role || "";
    state.isAdmin = (state.role || "").toLowerCase() === "admin";
    state.canExec = !!me.can_exec;
    state.canProcs = !!me.can_procs;
    state.canFW = !!me.can_firewall;
    const meNode = document.getElementById("me");
    if (meNode) meNode.textContent = state.me;
  })();
  try {
    await mePromise;
  } finally {
    mePromise = null;
  }
}

export async function api(path, options = {}) {
  const headers = new Headers(options.headers || {});
  const method = (options.method || "GET").toUpperCase();
  const needsCSRF = method !== "GET" && method !== "HEAD";
  if (needsCSRF && !state.csrf) await ensureMe();
  if (needsCSRF && state.csrf) headers.set("X-Atlas-CSRF", state.csrf);

  let res = await fetch(path, { ...options, headers });
  if (res.status === 403) {
    const text = await res.text().catch(() => "");
    if (needsCSRF && text.includes("csrf token required")) {
      await ensureMe(true);
      const retryHeaders = new Headers(options.headers || {});
      if (state.csrf) retryHeaders.set("X-Atlas-CSRF", state.csrf);
      res = await fetch(path, { ...options, headers: retryHeaders });
    } else {
      throw new Error(`${res.status} ${res.statusText}${text ? `: ${text}` : ""}`);
    }
  }
  if (!res.ok) {
    const text = await res.text().catch(() => "");
    throw new Error(`${res.status} ${res.statusText}${text ? `: ${text}` : ""}`);
  }

  const ct = res.headers.get("content-type") || "";
  if (ct.includes("application/json")) return res.json();
  return res.text();
}
