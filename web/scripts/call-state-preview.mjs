import { chromium } from "playwright-core";
import { mkdirSync } from "node:fs";
import { join } from "node:path";

const chrome = process.env.CHROME_PATH || "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe";
const outDir = join("C:\\Users\\Xian\\Projects\\VoCat\\web", "preview-shots");
mkdirSync(outDir, { recursive: true });

const chromePath = chrome;
const browser = await chromium.launch({ executablePath: chromePath, headless: true });

async function login(page) {
  await page.addInitScript(() => {
    localStorage.setItem("theme", "dark");
    localStorage.setItem("vocat_disclaimer_agreed_at", String(Date.now()));
    document.documentElement.classList.add("dark");
  });
  await page.goto("http://127.0.0.1:5173/login", { waitUntil: "networkidle" });
  const user = page.getByPlaceholder(/用户名|Username/i);
  if (await user.count()) {
    await user.fill("admin");
    await page.getByPlaceholder(/密码|Password/i).fill("admin");
    await page.getByRole("button", { name: /登录|Sign in|Log in/i }).click();
    await page.waitForTimeout(800);
  }
}

async function mockCalls(page, call) {
  await page.route("**/api/devices/*/calls", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ data: { status: "ok" } }) });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: { device_id: "ec25-uk", transport: "vowifi", calls: [call] } }),
    });
  });
}

const states = [
  {
    file: "10-call-dialing.png",
    call: { id: "c1", number: "+447911123456", direction: "outgoing", state: "dialing", started_at: new Date().toISOString() },
  },
  {
    file: "11-call-connected.png",
    call: { id: "c2", number: "+447911123456", direction: "outgoing", state: "active", media_ready: true, started_at: new Date(Date.now() - 86000).toISOString(), answered_at: new Date(Date.now() - 80000).toISOString(), codec: "AMR-WB" },
  },
  {
    file: "12-call-incoming.png",
    call: { id: "c3", number: "+447700900999", direction: "incoming", state: "ringing", started_at: new Date().toISOString() },
  },
];

for (const item of states) {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  await login(page);
  await mockCalls(page, item.call);
  await page.goto("http://127.0.0.1:5173/phone", { waitUntil: "networkidle" });
  await page.waitForTimeout(700);
  await page.screenshot({ path: join(outDir, item.file), fullPage: false });
  console.log("saved", item.file);
  await page.close();
}

await browser.close();
