import { chromium } from "playwright-core";
import fs from "node:fs";
import path from "node:path";

const BASE = "http://127.0.0.1:5173";
const OUT = path.resolve("scripts/motion-audit-out");
fs.mkdirSync(OUT, { recursive: true });
const chrome = "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe";

const browser = await chromium.launch({ executablePath: chrome, headless: true });
const page = await browser.newPage({ viewport: { width: 1366, height: 768 } });
page.setDefaultTimeout(15000);
const report = { checks: [], shots: [] };

function check(name, ok, extra = {}) {
  report.checks.push({ name, ok, ...extra });
}

try {
  await page.goto(`${BASE}/`, { waitUntil: "networkidle" });
  await page.waitForTimeout(500);
  const user = page.getByPlaceholder(/用户名|Username/i);
  if (await user.count()) {
    await user.fill("admin");
    await page.getByPlaceholder(/密码|Password/i).fill("admin");
    await page.getByRole("button", { name: /登录/i }).click();
    await page.waitForTimeout(1200);
  }
  const phrase = page.locator("input[placeholder*='请输入'], input[placeholder*='Please type']");
  if (await phrase.count()) {
    await phrase.fill("我同意并确认");
    await page.getByRole("button", { name: /同意并继续|Agree/ }).click({ force: true });
    await page.waitForTimeout(400);
  } else {
    const periodic = page.getByRole("button", { name: /我同意并确认|I agree and confirm/ });
    if (await periodic.count()) await periodic.click();
  }
  await page.waitForSelector(".vocat-app-shell");

  async function inspectDialog(label) {
    await page.waitForSelector('[role="dialog"]');
    await page.waitForTimeout(450);
    const info = await page.evaluate(() => {
      const dialog = document.querySelector('[role="dialog"]');
      const root = dialog?.closest(".halo-modal-root");
      if (!dialog || !root) return { missing: true };
      const dr = dialog.getBoundingClientRect();
      const rr = root.getBoundingClientRect();
      const parent = dialog.parentElement;
      const parentTag = parent?.tagName;
      const inBody = parent === document.body || root.parentElement === document.body;
      const cs = getComputedStyle(dialog);
      const overflow = {
        top: dr.top < 0,
        left: dr.left < 0,
        right: dr.right > window.innerWidth + 1,
        bottom: dr.bottom > window.innerHeight + 1,
      };
      return {
        inBody,
        parentTag,
        root: { x: rr.x, y: rr.y, w: rr.width, h: rr.height },
        dialog: { x: Math.round(dr.x), y: Math.round(dr.y), w: Math.round(dr.width), h: Math.round(dr.height) },
        viewport: { w: window.innerWidth, h: window.innerHeight },
        overflow,
        transform: cs.transform,
        zIndex: getComputedStyle(root).zIndex,
        border: cs.border,
        clipped: Object.values(overflow).some(Boolean),
      };
    });
    const shot = `${label}.png`;
    await page.screenshot({ path: path.join(OUT, shot) });
    report.shots.push(shot);
    check(label, !info.missing && info.inBody && !info.clipped, info);
    return info;
  }

  await page.getByRole("link", { name: "短信检测" }).click();
  await page.waitForTimeout(400);
  await page.getByRole("button", { name: /发送测试短信/ }).click();
  await inspectDialog("modal-sms");
  await page.keyboard.press("Escape");
  await page.waitForTimeout(200);

  await page.getByRole("link", { name: "自动任务" }).click();
  await page.waitForTimeout(400);
  await page.getByRole("button", { name: /添加任务/ }).click();
  await inspectDialog("modal-tasks");
  await page.keyboard.press("Escape");
  await page.waitForTimeout(200);

  await page.getByRole("link", { name: "代理管理" }).click();
  await page.waitForTimeout(400);
  await page.getByRole("button", { name: /新增代理/ }).click();
  await inspectDialog("modal-proxy");
  await page.keyboard.press("Escape");
} catch (error) {
  report.fatal = String(error);
  await page.screenshot({ path: path.join(OUT, "modal-error.png") }).catch(() => {});
} finally {
  await browser.close();
}

fs.writeFileSync(path.join(OUT, "modal-report.json"), JSON.stringify(report, null, 2));
console.log(JSON.stringify(report, null, 2));
if (report.fatal || report.checks.some((c) => !c.ok)) process.exit(1);
