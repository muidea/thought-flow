// ThoughtFlow i18n registry. Vanilla JS, no dependencies.
//
// Behavior:
//   - t(key, vars?)   → resolved string with `{name}` placeholders interpolated
//   - tn(key, count, vars?)  → pluralized string. Selects the
//     {n, plural, =0{...} one{...} other{...}} branch (a tiny CLDR subset) or
//     falls back to the first defined branch.
//   - init()         → resolve locale from URL query, then localStorage, then
//     navigator.language, then default ("zh-CN"). Writes <html lang> and
//     stores the choice.
//   - setLocale(loc) → switch and persist; fires "locale:changed" event.
//   - onLocaleChange(handler) → subscribe to switches.
//
// Missing keys fall back to en-US, then to the literal `[[key]]` with a
// `console.warn` so missing translations are easy to find in development.

import enUS from "./en-US.js";
import zhCN from "./zh-CN.js";

const DEFAULT_LOCALE = "zh-CN";
const STORAGE_KEY = "tflow.lang";

const locales = {
  "en-US": enUS,
  "zh-CN": zhCN,
};

const fallbackLocale = "en-US";

let currentLocale = DEFAULT_LOCALE;
const listeners = new Set();
let missingReported = new Set();

function lookupMessages(locale) {
  return locales[locale] || locales[fallbackLocale] || {};
}

function pickPluralBranch(template, count) {
  // Very small CLDR-like selector: =0{...} one{...} other{...}
  const branches = {};
  const re = /(=\d+)\{([^}]*)\}|\{([^}]*)\}/g;
  let match;
  let fallback = null;
  while ((match = re.exec(template)) !== null) {
    if (match[1]) {
      const value = Number(match[1].slice(1));
      if (Number.isFinite(value) && value === count) return match[2];
    } else {
      const key = match[3].trim();
      if (key === "other") fallback = match[3];
      else if (key === "one" && count === 1) return match[3];
      else if (key === "zero" && count === 0) return match[3];
      else if (key === "two" && count === 2) return match[3];
      else branches[key] = match[3];
    }
  }
  return fallback || branches.other || template;
}

function interpolate(template, vars) {
  if (!vars) return template;
  return template.replace(/\{(\w+)\}/g, (match, name) => {
    if (Object.prototype.hasOwnProperty.call(vars, name)) {
      return String(vars[name]);
    }
    return match;
  });
}

function reportMissing(key) {
  if (missingReported.has(key)) return;
  missingReported.add(key);
  if (typeof console !== "undefined" && console.warn) {
    console.warn(`[i18n] missing translation: ${key}`);
  }
}

function resolve(key, vars) {
  const chain = [currentLocale, fallbackLocale];
  for (const locale of chain) {
    const messages = lookupMessages(locale);
    if (Object.prototype.hasOwnProperty.call(messages, key)) {
      return interpolate(messages[key], vars);
    }
  }
  reportMissing(key);
  return `[[${key}]]`;
}

export function t(key, vars) {
  return resolve(key, vars);
}

export function tn(key, count, vars) {
  const raw = resolve(key, vars);
  if (typeof raw !== "string" || !raw.includes("{")) return raw;
  return pickPluralBranch(raw, count);
}

// Browser global exposure — app.js is loaded as a classic script (not a
// module) and reads from `window.tflow_i18n`. Tests provide their own stub
// via vm context. Server-side / Node `import` keeps the named exports above.
if (typeof window !== "undefined") {
  window.tflow_i18n = {
    t,
    tn,
    setLocale,
    getLocale,
    init,
    applyTranslations,
    onLocaleChange,
    listLocales,
    resetMissingReport,
  };
}

function detectLocale() {
  try {
    const search = typeof window !== "undefined" ? new URLSearchParams(window.location.search) : null;
    const fromQuery = search?.get("lang");
    if (fromQuery && locales[fromQuery]) return fromQuery;
  } catch (_error) {
    // ignore — non-browser context
  }
  try {
    const fromStorage = typeof window !== "undefined" ? window.localStorage?.getItem(STORAGE_KEY) : null;
    if (fromStorage && locales[fromStorage]) return fromStorage;
  } catch (_error) {
    // ignore — localStorage blocked
  }
  try {
    const nav = typeof navigator !== "undefined" ? navigator.language : null;
    if (nav) {
      if (locales[nav]) return nav;
      const lower = nav.toLowerCase();
      const matched = Object.keys(locales).find((loc) => loc.toLowerCase() === lower);
      if (matched) return matched;
      if (lower.startsWith("zh")) return "zh-CN";
      if (lower.startsWith("en")) return "en-US";
    }
  } catch (_error) {
    // ignore
  }
  return DEFAULT_LOCALE;
}

export function getLocale() {
  return currentLocale;
}

export function listLocales() {
  return Object.keys(locales);
}

export function setLocale(locale) {
  if (!locales[locale]) {
    reportMissing(`locale:${locale}`);
    return;
  }
  if (locale === currentLocale) return;
  currentLocale = locale;
  try {
    if (typeof window !== "undefined") {
      window.localStorage?.setItem(STORAGE_KEY, locale);
      if (document?.documentElement) {
        document.documentElement.lang = locale;
      }
    }
  } catch (_error) {
    // ignore
  }
  for (const handler of listeners) {
    try {
      handler(locale);
    } catch (error) {
      if (typeof console !== "undefined" && console.error) console.error("[i18n] listener error", error);
    }
  }
}

export function onLocaleChange(handler) {
  listeners.add(handler);
  return () => listeners.delete(handler);
}

export function resetMissingReport() {
  missingReported = new Set();
}

export function init(options = {}) {
  const requested = options.locale || detectLocale();
  if (locales[requested]) {
    currentLocale = requested;
  } else {
    currentLocale = DEFAULT_LOCALE;
  }
  try {
    if (typeof document !== "undefined" && document.documentElement) {
      document.documentElement.lang = currentLocale;
    }
  } catch (_error) {
    // ignore
  }
  return currentLocale;
}

// Apply translations to elements marked with `data-i18n` / `data-i18n-attr`.
// `data-i18n="key"`        → textContent = t(key)
// `data-i18n-attr="key"`   → setAttribute(attr, t(key)) where attr comes
//   from the `data-i18n-attr-target` attribute on the same node
//   (e.g. data-i18n-attr-target="placeholder").
// `data-i18n-template="key"` → textContent = t(key, vars) where vars are
//   built from the element's data-* siblings (data-n → {n}).
//   data-empty="true" marks empty-state containers; rendered text comes
//   from the template, not the original DOM text.
export function applyTranslations(root) {
  if (typeof document === "undefined") return;
  const scope = root || document;
  scope.querySelectorAll("[data-i18n]").forEach((node) => {
    const key = node.getAttribute("data-i18n");
    if (key) node.textContent = t(key);
  });
  scope.querySelectorAll("[data-i18n-attr]").forEach((node) => {
    const key = node.getAttribute("data-i18n-attr");
    const target = node.getAttribute("data-i18n-attr-target");
    if (key && target) node.setAttribute(target, t(key));
  });
  scope.querySelectorAll("[data-i18n-template]").forEach((node) => {
    const key = node.getAttribute("data-i18n-template");
    if (!key) return;
    const vars = {};
    for (const attr of Array.from(node.attributes)) {
      if (!attr.name.startsWith("data-") || attr.name === "data-i18n-template" || attr.name === "data-empty") continue;
      const name = attr.name.slice(5);
      if (!name) continue;
      const raw = attr.value;
      if (/^-?\d+(\.\d+)?$/.test(raw)) vars[name] = Number(raw);
      else vars[name] = raw;
    }
    node.textContent = t(key, vars);
  });
}
