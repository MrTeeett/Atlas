export function fmtBytes(n) {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let v = Number(n) || 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v >= 10 || i === 0 ? 0 : 1)} ${units[i]}`;
}

export function fmtRate(bytesPerSecond) {
  return `${fmtBytes(bytesPerSecond)}/s`;
}

export function fmtPct(v, digits = 1) {
  if (v === null || v === undefined) return "—";
  return `${Number(v).toFixed(digits)}%`;
}

export function fmtDate(unixSeconds) {
  const d = new Date((Number(unixSeconds) || 0) * 1000);
  if (!unixSeconds) return "—";
  return d.toLocaleString(undefined, { year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" });
}

export function fmtUptime(seconds) {
  const s = Math.max(0, Math.floor(Number(seconds) || 0));
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return `${d}d ${h}h ${m}m`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

