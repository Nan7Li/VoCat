try {
  var theme = localStorage.getItem("theme");
  var dark = theme === "dark";
  var root = document.documentElement;
  root.classList.toggle("dark", dark);
  root.style.background = dark ? "#1A1610" : "#F7F4EF";

  var stored = localStorage.getItem("halo.accent") || "#E85D3C";
  var raw = String(stored).replace("#", "");
  if (/^[0-9a-fA-F]{3}$/.test(raw)) raw = raw.split("").map(function (ch) { return ch + ch; }).join("");
  if (!/^[0-9a-fA-F]{6}$/.test(raw)) raw = "E85D3C";
  var n = parseInt(raw, 16);
  var r = (n >> 16) & 255;
  var g = (n >> 8) & 255;
  var b = n & 255;
  var mix = function (t) {
    var rr = Math.max(0, Math.min(255, Math.round(r + (0 - r) * t)));
    var gg = Math.max(0, Math.min(255, Math.round(g + (0 - g) * t)));
    var bb = Math.max(0, Math.min(255, Math.round(b + (0 - b) * t)));
    return "#" + [rr, gg, bb].map(function (v) { return v.toString(16).padStart(2, "0"); }).join("");
  };
  var srgb = [r, g, b].map(function (v) {
    v = v / 255;
    return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4);
  });
  var lum = 0.2126 * srgb[0] + 0.7152 * srgb[1] + 0.0722 * srgb[2];
  var hex = "#" + raw.toUpperCase();
  root.style.setProperty("--color-primary", hex);
  root.style.setProperty("--color-primary-hover", mix(0.16));
  root.style.setProperty("--color-primary-active", mix(0.28));
  root.style.setProperty("--color-primary-ink", mix(0.22));
  root.style.setProperty("--color-primary-soft", "rgba(" + r + ", " + g + ", " + b + ", 0.16)");
  root.style.setProperty("--color-primary-faint", "rgba(" + r + ", " + g + ", " + b + ", 0.08)");
  root.style.setProperty("--color-primary-border", "rgba(" + r + ", " + g + ", " + b + ", 0.32)");
  root.style.setProperty("--color-primary-rgb", r + " " + g + " " + b);
  root.style.setProperty("--color-on-primary", lum > 0.45 ? "#2C2C2C" : "#FFFFFF");
  root.style.setProperty("--el-color-primary", hex);
  root.style.setProperty("--el-color-primary-dark-2", mix(0.16));
} catch (e) {}
