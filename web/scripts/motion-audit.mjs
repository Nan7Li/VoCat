import { chromium } from "playwright-core";
import fs from "node:fs";
import path from "node:path";

const BASE = "http://127.0.0.1:5173";
const OUT = path.resolve("scripts/motion-audit-out");
fs.mkdirSync(OUT, { recursive: true });

const chrome =
  process.env.CHROME_PATH ||
  "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe";

function median(values) {
  if (!values.length) return 0;
  const sorted = [...values].sort((a, b) => a - b);
  const mid = Math.floor(sorted.length / 2);
  return sorted.length % 2 ? sorted[mid] : (sorted[mid - 1] + sorted[mid]) / 2;
}

async function measureFrames(page, action, ms = 700) {
  await page.evaluate(() => {
    window.__frames = [];
    window.__raf = true;
    let last = performance.now();
    const tick = (now) => {
      if (!window.__raf) return;
      window.__frames.push(now - last);
      last = now;
      requestAnimationFrame(tick);
    };
    requestAnimationFrame(tick);
  });
  await action();
  await page.waitForTimeout(ms);
  const frames = await page.evaluate(() => {
    window.__raf = false;
    return window.__frames.slice(1);
  });
  const long = frames.filter((d) => d > 22);
  return {
    samples: frames.length,
    avg: Number((frames.reduce((a, b) => a + b, 0) / (frames.length || 1)).toFixed(2)),
    median: Number(median(frames).toFixed(2)),
    max: Number(Math.max(0, ...frames).toFixed(2)),
    longFrames: long.length,
    longPct: Number(((long.length / (frames.length || 1)) * 100).toFixed(1)),
  };
}

async function shot(page, name) {
  await page.screenshot({ path: path.join(OUT, `${name}.png`), fullPage: false });
}

const report = { pages: [], motion: {}, issues: [], notes: [] };

const browser = await chromium.launch({
  executablePath: chrome,
  headless: true,
  args: ["--disable-dev-shm-usage"],
});
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
page.setDefaultTimeout(15000);

try {
  await page.goto(`${BASE}/`, { waitUntil: "networkidle" });
  await page.waitForTimeout(600);
  await shot(page, "01-start");

  const loginBg = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  report.notes.push(`start body background: ${loginBg}`);

  const user = page.getByPlaceholder(/用户名|Username/i);
  if (await user.count()) {
    await user.fill("admin");
    await page.getByPlaceholder(/密码|Password/i).fill("admin");
    await page.getByRole("button", { name: /登录|Sign in|Log in/i }).click();
    await page.waitForTimeout(1200);
    await shot(page, "01b-after-login");
  }

  const phrase = page.locator("input[placeholder*='请输入'], input[placeholder*='Please type']");
  if (await phrase.count()) {
    await phrase.fill("我同意并确认");
    await page.getByRole("button", { name: /同意并继续|Agree/ }).click({ force: true });
    await page.waitForTimeout(500);
  } else {
    const periodic = page.getByRole("button", { name: /我同意并确认|I agree and confirm/ });
    if (await periodic.count()) {
      await periodic.click();
      await page.waitForTimeout(400);
    }
  }

  await page.waitForSelector(".vocat-app-shell", { timeout: 10000 });
  await page.waitForTimeout(500);
  await shot(page, "02-dashboard");
  report.pages.push("dashboard");

  const tokens = await page.evaluate(() => {
    const root = getComputedStyle(document.documentElement);
    const body = getComputedStyle(document.body);
    const card = document.querySelector(".ui-card");
    const sidebar = document.querySelector(".vocat-sidebar");
    const menu = document.querySelector(".vocat-menu-item.is-active");
    return {
      primary: root.getPropertyValue("--color-primary").trim(),
      grouped: root.getPropertyValue("--color-grouped").trim(),
      bodyBg: body.backgroundColor,
      cardRadius: card ? getComputedStyle(card).borderRadius : null,
      cardShadow: card ? getComputedStyle(card).boxShadow : null,
      sidebarBg: sidebar ? getComputedStyle(sidebar).backgroundColor : null,
      activeMenuRadius: menu ? getComputedStyle(menu).borderRadius : null,
      pageAnim: document.querySelector(".vocat-page")
        ? getComputedStyle(document.querySelector(".vocat-page")).animationName
        : null,
    };
  });
  report.tokens = tokens;

  const audit = await page.evaluate(() => {
    const issues = [];
    const sheets = [...document.styleSheets];
    let sheen = false;
    let mesh = false;
    let blurEnter = false;
    for (const sheet of sheets) {
      let rules = [];
      try {
        rules = [...sheet.cssRules];
      } catch {
        continue;
      }
      for (const rule of rules) {
        const text = rule.cssText || "";
        if (text.includes("glass-sheen")) sheen = true;
        if (text.includes("mesh-drift")) mesh = true;
        if ((text.includes("ios-rise") || text.includes("ios-page")) && text.includes("blur(")) blurEnter = true;
      }
    }
    if (sheen) issues.push("glass-sheen still present");
    if (mesh) issues.push("mesh-drift still present");
    if (blurEnter) issues.push("enter animation still uses filter blur");

    const infinite = [];
    for (const el of document.querySelectorAll("*")) {
      const anim = getComputedStyle(el).animationName;
      const iter = getComputedStyle(el).animationIterationCount;
      if (anim && anim !== "none" && iter === "infinite") {
        const cls = el.className?.toString?.() || el.tagName;
        if (!String(cls).includes("boot") && !String(cls).includes("spin") && !String(cls).includes("pulse")) {
          infinite.push(cls.slice(0, 80));
        }
      }
    }
    return { issues, infiniteChrome: [...new Set(infinite)].slice(0, 12) };
  });
  report.issues.push(...audit.issues);
  report.infiniteChrome = audit.infiniteChrome;

  report.motion.sidebar = await measureFrames(page, async () => {
    await page.getByRole("button", { name: /收起侧栏|展开侧栏/ }).click();
  }, 800);
  await shot(page, "03-sidebar-collapsed");
  await page.getByRole("button", { name: /收起侧栏|展开侧栏/ }).click();
  await page.waitForTimeout(450);

  const nav = [
    { name: "设备管理", file: "04-devices" },
    { name: "短信检测", file: "05-sms" },
    { name: "系统设置", file: "06-settings" },
  ];
  for (const item of nav) {
    report.motion[`nav-${item.name}`] = await measureFrames(page, async () => {
      await page.getByRole("link", { name: item.name }).click();
    }, 700);
    await shot(page, item.file);
    report.pages.push(item.name);
  }

  report.motion.theme = await measureFrames(page, async () => {
    await page.getByRole("button", { name: /切换深色模式|切换浅色模式/ }).click();
  }, 800);
  await shot(page, "07-settings-dark");
  await page.getByRole("button", { name: /切换深色模式|切换浅色模式/ }).click();
  await page.waitForTimeout(400);

  const apricot = page.getByRole("button", { name: /杏橙|Apricot/ });
  if (await apricot.count()) {
    report.motion.accent = await measureFrames(page, async () => {
      await apricot.click();
    }, 700);
  }

  const gold = page.getByRole("button", { name: /Grok 金|Grok gold/ });
  if (await gold.count()) {
    await gold.click();
    await page.waitForTimeout(400);
    await apricot.click();
  }

  const notifyTab = page.getByRole("button", { name: /飞书|Lark/ });
  if (await notifyTab.count()) {
    report.motion.segment = await measureFrames(page, async () => {
      await notifyTab.first().click();
    }, 600);
    await shot(page, "08-settings-lark");
  }

  await page.getByRole("link", { name: "仪表盘" }).click();
  await page.waitForTimeout(400);
  await shot(page, "09-dashboard-return");

  const styleOk =
    tokens.primary.toUpperCase().includes("E85D3C") ||
    tokens.primary.toUpperCase() === "#E85D3C";
  if (!styleOk) report.issues.push(`primary is ${tokens.primary}, expected #E85D3C`);
  if (!tokens.grouped.toUpperCase().includes("F7F4EF")) {
    report.issues.push(`grouped is ${tokens.grouped}, expected #F7F4EF`);
  }

  const janky = Object.entries(report.motion).filter(([, m]) => m.longPct > 20 && m.max > 40);
  if (janky.length) {
    report.issues.push(`janky interactions: ${janky.map(([k, m]) => `${k} max=${m.max} long=${m.longPct}%`).join("; ")}`);
  }
} catch (error) {
  report.fatal = String(error);
  await shot(page, "99-error").catch(() => {});
} finally {
  await browser.close();
}

fs.writeFileSync(path.join(OUT, "report.json"), JSON.stringify(report, null, 2));
console.log(JSON.stringify(report, null, 2));
if (report.fatal) process.exit(1);
