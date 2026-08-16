import { useEffect, useState } from "react";

export const ACCENT_KEY = "halo.accent";
export const DEFAULT_ACCENT = "#C9A46A";

export const ACCENT_PRESETS = [
  { id: "grok", label: "Grok 金", hex: "#C9A46A" },
  { id: "cream", label: "奶油", hex: "#E4D2B0" },
  { id: "copper", label: "赤铜", hex: "#C47A4A" },
  { id: "graphite", label: "石墨", hex: "#8E8A82" },
  { id: "ios", label: "系统蓝", hex: "#007AFF" },
] as const;

type RGB = { r: number; g: number; b: number };

function clamp(value: number) {
  return Math.max(0, Math.min(255, Math.round(value)));
}

export function normalizeHex(value: string): string | null {
  const raw = value.trim().replace("#", "");
  if (!/^[0-9a-fA-F]{3}$|^[0-9a-fA-F]{6}$/.test(raw)) return null;
  const hex = raw.length === 3 ? raw.split("").map((ch) => ch + ch).join("") : raw;
  return `#${hex.toUpperCase()}`;
}

function parseHex(hex: string): RGB | null {
  const normalized = normalizeHex(hex);
  if (!normalized) return null;
  const n = Number.parseInt(normalized.slice(1), 16);
  return { r: (n >> 16) & 255, g: (n >> 8) & 255, b: n & 255 };
}

function toHex({ r, g, b }: RGB) {
  return `#${[r, g, b].map((value) => clamp(value).toString(16).padStart(2, "0")).join("")}`.toUpperCase();
}

function mix(from: RGB, to: RGB, amount: number): RGB {
  return {
    r: clamp(from.r + (to.r - from.r) * amount),
    g: clamp(from.g + (to.g - from.g) * amount),
    b: clamp(from.b + (to.b - from.b) * amount),
  };
}

function luminance({ r, g, b }: RGB) {
  const channel = [r, g, b].map((value) => {
    const srgb = value / 255;
    return srgb <= 0.03928 ? srgb / 12.92 : ((srgb + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * channel[0] + 0.7152 * channel[1] + 0.0722 * channel[2];
}

export function readStoredAccent() {
  try {
    return normalizeHex(localStorage.getItem(ACCENT_KEY) || "") || DEFAULT_ACCENT;
  } catch {
    return DEFAULT_ACCENT;
  }
}

export function applyAccent(hex: string, persist = true) {
  const rgb = parseHex(hex);
  if (!rgb) return DEFAULT_ACCENT;
  const value = toHex(rgb);
  const hover = toHex(mix(rgb, { r: 0, g: 0, b: 0 }, 0.16));
  const active = toHex(mix(rgb, { r: 0, g: 0, b: 0 }, 0.28));
  const root = document.documentElement;
  root.style.setProperty("--color-primary", value);
  root.style.setProperty("--color-primary-hover", hover);
  root.style.setProperty("--color-primary-active", active);
  root.style.setProperty("--color-primary-soft", `rgba(${rgb.r}, ${rgb.g}, ${rgb.b}, 0.16)`);
  root.style.setProperty("--color-primary-rgb", `${rgb.r} ${rgb.g} ${rgb.b}`);
  root.style.setProperty("--color-on-primary", luminance(rgb) > 0.45 ? "#1A1610" : "#FFFFFF");
  root.style.setProperty("--el-color-primary", value);
  root.style.setProperty("--el-color-primary-dark-2", hover);
  root.style.setProperty("--ui-btn-primary-shadow", `0 6px 18px rgba(${rgb.r}, ${rgb.g}, ${rgb.b}, 0.28)`);
  root.style.setProperty("--ui-btn-primary-shadow-hover", `0 8px 22px rgba(${rgb.r}, ${rgb.g}, ${rgb.b}, 0.36)`);
  if (persist) {
    try {
      localStorage.setItem(ACCENT_KEY, value);
    } catch {
      /* ignore */
    }
  }
  return value;
}

export function useAccent() {
  const [accent, setAccentState] = useState(DEFAULT_ACCENT);
  useEffect(() => {
    setAccentState(applyAccent(readStoredAccent(), false));
  }, []);
  function setAccent(hex: string) {
    setAccentState(applyAccent(hex));
  }
  return { accent, setAccent };
}
