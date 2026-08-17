import { chromium } from "playwright-core";

const chrome = "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe";
const browser = await chromium.launch({ executablePath: chrome, headless: true });
const page = await browser.newPage({ viewport: { width: 1366, height: 768 } });

await page.goto("http://127.0.0.1:5173/", { waitUntil: "networkidle" });
const user = page.getByPlaceholder(/用户名|Username/i);
if (await user.count()) {
  await user.fill("admin");
  await page.getByPlaceholder(/密码|Password/i).fill("admin");
  await page.getByRole("button", { name: /登录/ }).click();
  await page.waitForTimeout(1000);
}
await page.waitForTimeout(600);
const overlay = page.locator(".disclaimer-overlay");
if (await overlay.isVisible().catch(() => false)) {
  const typed = overlay.locator("input");
  if (await typed.count()) {
    const needed = (await overlay.locator(".select-all").textContent())?.trim() || "我同意并确认";
    await typed.fill(needed);
    await page.waitForTimeout(150);
  }
  const agree = overlay.locator("button:not([disabled])").filter({ hasText: /同意|Agree|确认|confirm/i });
  if (await agree.count()) await agree.first().click({ force: true });
  else await overlay.locator("button").last().click({ force: true });
  await overlay.waitFor({ state: "hidden", timeout: 5000 }).catch(() => {});
}

async function sample(label) {
  return page.evaluate((name) => {
    const root = getComputedStyle(document.documentElement);
    const active = document.querySelector(".vocat-menu-item.is-active");
    const btn = document.querySelector("button.ui-action-btn-primary, .ui-action-btn-primary");
    const primaryBtn = [...document.querySelectorAll("button")].find((el) => el.className.includes("bg-[var(--color-primary)]") || getComputedStyle(el).backgroundColor.includes("232, 93, 60") || el.textContent?.includes("检查更新") || el.textContent?.includes("更新凭证"));
    return {
      name,
      cssPrimary: root.getPropertyValue("--color-primary").trim(),
      cssRgb: root.getPropertyValue("--color-primary-rgb").trim(),
      activeBg: active ? getComputedStyle(active).backgroundColor : null,
      activeColor: active ? getComputedStyle(active).color : null,
      updateBtnBg: primaryBtn ? getComputedStyle(primaryBtn).backgroundColor : null,
    };
  }, label);
}

await page.getByRole("link", { name: "系统设置" }).click();
await page.waitForTimeout(400);
const before = await sample("apricot");

await page.getByRole("button", { name: /Grok 金/ }).click();
await page.waitForTimeout(500);
const gold = await sample("gold");

await page.getByRole("button", { name: /系统蓝/ }).click();
await page.waitForTimeout(500);
const blue = await sample("blue");

await page.getByRole("button", { name: /杏橙/ }).click();
await browser.close();

const report = { before, gold, blue };
const ok =
  gold.cssPrimary.toUpperCase() === "#C9A46A" &&
  blue.cssPrimary.toUpperCase() === "#007AFF" &&
  gold.activeBg !== before.activeBg &&
  blue.activeBg !== gold.activeBg;
console.log(JSON.stringify({ ok, ...report }, null, 2));
if (!ok) process.exit(1);
