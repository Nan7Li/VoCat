import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import vm from "node:vm";

const source = await readFile(new URL("../public/theme-init.js", import.meta.url), "utf8");

function initializeTheme(storedTheme, systemDark) {
  const classes = new Set();
  const styles = new Map();
  const root = {
    classList: {
      toggle(name, enabled) {
        if (enabled) classes.add(name);
        else classes.delete(name);
      },
    },
    style: {
      background: "",
      setProperty(name, value) {
        styles.set(name, value);
      },
    },
  };
  vm.runInNewContext(source, {
    document: { documentElement: root },
    localStorage: {
      getItem(key) {
        if (key === "theme") return storedTheme;
        return null;
      },
    },
    window: { matchMedia: () => ({ matches: systemDark }) },
  });
  return { dark: classes.has("dark"), background: root.style.background, styles };
}

test("uses the system color scheme when no explicit theme is stored", () => {
  assert.equal(initializeTheme(null, true).dark, true);
  assert.equal(initializeTheme(null, false).dark, false);
  assert.equal(initializeTheme("system", true).background, "#1A1610");
});

test("an explicit theme overrides the system color scheme", () => {
  assert.equal(initializeTheme("light", true).dark, false);
  assert.equal(initializeTheme("dark", false).dark, true);
});
