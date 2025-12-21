import { en } from "./i18n/en.js";
import { ru } from "./i18n/ru.js";

const LS_KEY = "atlas.lang";

export const LANGS = [
  "en",
  "ru",
];

const DICT = { en, ru };
let current = null;

export function getLang() {
  const v = (localStorage.getItem(LS_KEY) || "").trim().toLowerCase();
  if (v && DICT[v]) return v;
  const nav = (navigator.language || "").toLowerCase();
  if (nav.startsWith("ru")) return "ru";
  return "en";
}

export function setLang(lang) {
  const id = DICT[lang] ? lang : "en";
  current = id;
  try { localStorage.setItem(LS_KEY, id); } catch {}
  document.documentElement.lang = id;
  window.dispatchEvent(new Event("atlas:lang"));
}

export function initLang() {
  setLang(getLang());
}

function get(obj, k) {
  return k.split(".").reduce((acc, part) => (acc && acc[part] != null ? acc[part] : null), obj);
}

export function t(key, vars = null) {
  const lang = current || getLang();
  let v = get(DICT[lang], key);
  if (v == null) v = get(DICT.en, key);
  if (v == null) return key;
  v = String(v);
  if (vars && typeof vars === "object") {
    for (const [k, val] of Object.entries(vars)) {
      v = v.replaceAll(`{${k}}`, String(val));
    }
  }
  return v;
}
