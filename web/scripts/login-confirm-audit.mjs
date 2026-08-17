import { chromium } from "playwright-core";
import fs from "node:fs";
import path from "node:path";

const BASE = "http://127.0.0.1:5173";
const OUT = path.resolve("scripts/login-confirm-audit-out");
fs.mkdirSync(OUT, { recursive: true });
const chrome = process.env.CHROME_PATH || "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe";

const browser = await chromium.launch({
  executablePath: chrome,
  headless: true,
});
const context = await browser.newContext({ viewport: { width: 1280, height: 800 } });
const page = await context.newPage();
await page.route("**/api/auth/session", async (route) => {
  await route.fulfill({
    status: 200,
    contentType: "application/json",
    body: JSON.stringify({ data: { authenticated: false } }),
  });
});
await page.goto(`${BASE}/login`, { waitUntil: "networkidle" });
await page.getByPlaceholder(/用户名|Username/i).waitFor({ timeout: 8000 });
await page.waitForTimeout(500);
await page.screenshot({ path: path.join(OUT, "login-desktop.png"), fullPage: true });

await page.setViewportSize({ width: 390, height: 844 });
await page.waitForTimeout(200);
await page.screenshot({ path: path.join(OUT, "login-mobile.png"), fullPage: true });

await page.setViewportSize({ width: 1280, height: 800 });
await page.evaluate(() => {
  document.documentElement.classList.add("dark");
});
await page.waitForTimeout(200);
await page.screenshot({ path: path.join(OUT, "login-dark.png"), fullPage: true });
await page.evaluate(() => document.documentElement.classList.remove("dark"));

await page.evaluate(() => {
  const root = document.createElement("div");
  root.className = "halo-modal-root";
  root.style.zIndex = "10000";
  root.innerHTML = `
    <button type="button" class="halo-modal-backdrop"></button>
    <div role="alertdialog" aria-modal="true" class="halo-modal-panel glass-modal ui-pop w-full max-w-sm px-6 py-5">
      <div class="flex items-start gap-3">
        <div class="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center text-amber-500">!</div>
        <div class="min-w-0 flex-1">
          <div class="text-[17px] font-semibold tracking-tight text-[#2C2C2C]">提示</div>
          <div class="mt-2 text-[14px] leading-relaxed text-[#6B5C4F]">确认退出登录？</div>
        </div>
      </div>
      <div class="mt-5 flex items-center justify-end gap-2.5">
        <button type="button" class="inline-flex h-9 items-center rounded-full border border-[#E8D9C8] bg-white px-4 text-[13px] font-semibold">取消</button>
        <button type="button" class="inline-flex h-9 items-center rounded-full bg-[var(--color-primary)] px-4 text-[13px] font-semibold text-white">退出</button>
      </div>
    </div>
  `;
  document.body.appendChild(root);
});
await page.waitForTimeout(200);
await page.screenshot({ path: path.join(OUT, "confirm-dialog.png") });

const strip = await page.evaluate(() => {
  const last = document.querySelector(".halo-modal-panel > div:last-child");
  if (!last) return null;
  const style = getComputedStyle(last);
  return { bg: style.backgroundColor, borderTop: style.borderTopColor, borderTopWidth: style.borderTopWidth };
});
fs.writeFileSync(path.join(OUT, "confirm-footer-style.json"), JSON.stringify(strip, null, 2));
await browser.close();
console.log("wrote", OUT);
console.log("confirm last-child style", strip);
