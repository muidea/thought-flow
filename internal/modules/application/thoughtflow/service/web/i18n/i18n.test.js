// Tests for the i18n registry. Imports ESM modules from disk using
// file:// URLs because the i18n files are written as ESM.
const assert = require("node:assert/strict");
const path = require("node:path");
const { pathToFileURL } = require("node:url");
const test = require("node:test");

const i18nPath = pathToFileURL(
  path.join(__dirname, "index.js"),
).href;
const enUSPath = pathToFileURL(
  path.join(__dirname, "en-US.js"),
).href;
const zhCNPath = pathToFileURL(
  path.join(__dirname, "zh-CN.js"),
).href;

test("i18n registry resolves a key in zh-CN", async () => {
  const { init, t, setLocale, getLocale } = await import(i18nPath);
  init({ locale: "zh-CN" });
  setLocale("zh-CN");
  assert.equal(getLocale(), "zh-CN");
  assert.equal(t("nav.dashboard"), "仪表盘");
  assert.equal(t("common.cancel"), "取消");
  assert.equal(t("common.save"), "保存");
});

test("i18n registry falls back to en-US when a key is missing in zh-CN", async () => {
  const { init, t, setLocale } = await import(i18nPath);
  init({ locale: "en-US" });
  setLocale("en-US");
  assert.equal(t("nav.dashboard"), "Dashboard");
});

test("i18n registry interpolates {name} placeholders", async () => {
  const { t, init, setLocale } = await import(i18nPath);
  init({ locale: "en-US" });
  setLocale("en-US");
  const rendered = t("toast.captured", { id: "abc-123" });
  assert.equal(rendered, "Captured abc-123");
});

test("i18n registry exposes both en-US and zh-CN locales", async () => {
  const { listLocales } = await import(i18nPath);
  const locales = listLocales();
  assert.ok(locales.includes("en-US"));
  assert.ok(locales.includes("zh-CN"));
});

test("en-US and zh-CN cover the same set of keys", async () => {
  const en = await import(enUSPath);
  const zh = await import(zhCNPath);
  const enKeys = new Set(Object.keys(en.messages));
  const zhKeys = new Set(Object.keys(zh.messages));
  assert.equal(enKeys.size, zhKeys.size, "en-US and zh-CN have different key counts");
  for (const key of enKeys) {
    assert.ok(zhKeys.has(key), `zh-CN is missing translation for ${key}`);
  }
  for (const key of zhKeys) {
    assert.ok(enKeys.has(key), `en-US has unknown key ${key}`);
  }
});
