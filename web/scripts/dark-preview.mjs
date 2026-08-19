import { chromium } from "playwright-core";
import { mkdirSync } from "node:fs";
import { join } from "node:path";

const chrome = process.env.CHROME_PATH || "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe";
const outDir = join("C:\\Users\\Xian\\Projects\\VoCat\\web", "preview-shots");
mkdirSync(outDir, { recursive: true });

const browser = await chromium.launch({ executablePath: chrome, headless: true });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1 });

async function shot(name) {
  await page.waitForTimeout(400);
  const path = join(outDir, `${name}.png`);
  await page.screenshot({ path, fullPage: false });
  console.log("saved", path);
}

await page.addInitScript(() => {
  localStorage.setItem("theme", "dark");
  localStorage.setItem("vocat_disclaimer_agreed_at", String(Date.now()));
  document.documentElement.classList.add("dark");
});

await page.goto("http://127.0.0.1:5173/login", { waitUntil: "networkidle" });
await shot("01-login");

const user = page.getByPlaceholder(/用户名|Username/i);
if (await user.count()) {
  await user.fill("admin");
  await page.getByPlaceholder(/密码|Password/i).fill("admin");
  await page.locator('input[placeholder*="用户名"], input[placeholder*="Username"]').evaluate((el) => {
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await page.getByRole("button", { name: /登录|Sign in|Log in/i }).click();
  await page.waitForTimeout(1200);
}

const overlay = page.locator(".disclaimer-overlay");
if (await overlay.isVisible().catch(() => false)) {
  const typed = overlay.locator("input");
  if (await typed.count()) {
    const needed = (await overlay.locator(".select-all").textContent())?.trim() || "我同意并确认";
    await typed.fill(needed);
    await page.waitForTimeout(200);
  }
  const agree = overlay.locator("button:not([disabled])").filter({ hasText: /同意|Agree|确认|confirm/i });
  if (await agree.count()) await agree.first().click({ force: true });
  else await overlay.locator("button").last().click({ force: true });
  await overlay.waitFor({ state: "hidden", timeout: 5000 }).catch(() => {});
}

await page.goto("http://127.0.0.1:5173/", { waitUntil: "networkidle" });
await shot("02-dashboard");

await page.goto("http://127.0.0.1:5173/devices?device=ec25-uk&tab=overview", { waitUntil: "networkidle" });
await page.waitForTimeout(800);
await shot("03-devices-overview");

await page.goto("http://127.0.0.1:5173/devices?device=ec25-uk&tab=esim", { waitUntil: "networkidle" });
await page.waitForTimeout(800);
await shot("04-devices-esim");

await page.goto("http://127.0.0.1:5173/phone", { waitUntil: "networkidle" });
await page.waitForTimeout(600);
await shot("05-phone");

await page.goto("http://127.0.0.1:5173/sms", { waitUntil: "networkidle" });
await page.waitForTimeout(800);
await shot("06-sms");

await page.goto("http://127.0.0.1:5173/proxy", { waitUntil: "networkidle" });
await page.waitForTimeout(500);
await shot("07-proxy");

const mobile = await browser.newPage({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2 });
await mobile.addInitScript(() => {
  localStorage.setItem("theme", "dark");
  localStorage.setItem("vocat_disclaimer_agreed_at", String(Date.now()));
  document.documentElement.classList.add("dark");
});
await mobile.goto("http://127.0.0.1:5173/login", { waitUntil: "networkidle" });
const mu = mobile.getByPlaceholder(/用户名|Username/i);
if (await mu.count()) {
  await mu.fill("admin");
  await mobile.getByPlaceholder(/密码|Password/i).fill("admin");
  await mobile.getByRole("button", { name: /登录|Sign in|Log in/i }).click();
  await mobile.waitForTimeout(1200);
}
await mobile.goto("http://127.0.0.1:5173/devices?device=ec25-uk&tab=esim", { waitUntil: "networkidle" });
await mobile.waitForTimeout(800);
await mobile.screenshot({ path: join(outDir, "08-esim-mobile.png") });
console.log("saved", join(outDir, "08-esim-mobile.png"));

await mobile.goto("http://127.0.0.1:5173/phone", { waitUntil: "networkidle" });
await mobile.waitForTimeout(600);
await mobile.screenshot({ path: join(outDir, "09-phone-mobile.png") });
console.log("saved", join(outDir, "09-phone-mobile.png"));

await browser.close();
