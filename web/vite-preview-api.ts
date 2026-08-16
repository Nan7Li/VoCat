import type { IncomingMessage, ServerResponse } from "node:http";
import type { Plugin } from "vite";

type SessionState = {
  authenticated: boolean;
  username: string;
  role: string;
};

const session: SessionState = {
  authenticated: false,
  username: "",
  role: "Administrator",
};

const CSRF = "preview-csrf-token";

function send(res: ServerResponse, data: unknown, status = 200) {
  res.statusCode = status;
  res.setHeader("Content-Type", "application/json; charset=utf-8");
  res.setHeader("Cache-Control", "no-store");
  res.end(JSON.stringify({ data }));
}

function readBody(req: IncomingMessage): Promise<string> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = [];
    req.on("data", (chunk) => chunks.push(Buffer.from(chunk)));
    req.on("end", () => resolve(Buffer.concat(chunks).toString("utf8")));
    req.on("error", reject);
  });
}

function pathOf(url = "") {
  return url.split("?")[0] || "";
}

function previewDevice(id: string, name: string, healthy: boolean, vowifi: boolean) {
  return {
    id,
    name,
    device_type: "pcie_ec20_ec25",
    interface: "wwan0",
    proxy_port: 1080,
    public_ip: healthy ? "51.89.12.10" : "",
    healthy,
    operator: vowifi ? "Vodafone UK" : "",
    signal_dbm: healthy ? -81 : 0,
    network_mode: healthy ? "LTE" : "",
    network_duplex: healthy ? "FDD" : "",
    vowifi_active: vowifi,
    network_connected: healthy,
    model: "EC25",
    vowifi_runtime: {
      device_id: id,
      phase: vowifi ? "sms_ready" : "idle",
      dataplane_mode: "userspace",
      iccid: "",
      imsi: "",
      sim_ready: healthy,
      access_ready: healthy,
      tunnel_ready: vowifi,
      ims_ready: vowifi,
      sms_ready: vowifi,
      reg_status: healthy ? 1 : 0,
      reg_status_text: healthy ? "registered" : "unknown",
      network_mode: healthy ? "LTE" : "",
      last_error_class: "",
      last_error: "",
      last_reason: "",
      updated_at: new Date().toISOString(),
    },
  };
}

export function previewApiPlugin(): Plugin {
  return {
    name: "vocat-preview-api",
    configureServer(server) {
      server.middlewares.use(async (req, res, next) => {
        const path = pathOf(req.url || "");
        if (!path.startsWith("/api")) {
          next();
          return;
        }

        const method = (req.method || "GET").toUpperCase();

        try {
          if (path === "/api/auth/session" && method === "GET") {
            send(res, {
              authenticated: session.authenticated,
              username: session.username,
              role: session.role,
              csrf_token: CSRF,
            });
            return;
          }

          if (path === "/api/auth/login" && method === "POST") {
            const raw = await readBody(req);
            let body: { username?: string; password?: string } = {};
            try {
              body = JSON.parse(raw || "{}") as { username?: string; password?: string };
            } catch {
              body = {};
            }
            session.authenticated = true;
            session.username = body.username || "admin";
            session.role = "Administrator";
            send(res, {
              status: "ok",
              username: session.username,
              role: session.role,
              csrf_token: CSRF,
            });
            return;
          }

          if (path === "/api/auth/logout" && method === "POST") {
            session.authenticated = false;
            session.username = "";
            send(res, { status: "ok" });
            return;
          }

          if (path === "/api/system/info") {
            send(res, {
              version: "1.0.1",
              developer: false,
              hostname: "preview",
            });
            return;
          }

          if (path === "/api/dashboard/devices") {
            send(res, [
              previewDevice("ec25-uk", "EC25-UK", true, true),
              previewDevice("ec25-off", "EC25-OFF", false, false),
            ]);
            return;
          }

          if (path === "/api/dashboard/host") {
            send(res, {
              host: {
                cpu_model: "Apple Silicon",
                board_model: "LibWrt ipq60xx",
                memory_model: "LPDDR4",
                disk_model: "eMMC",
              },
              perf: {
                cpu_percent: 12.4,
                memory_percent: 38.1,
                memory_used_bytes: 1.6 * 1024 ** 3,
                memory_total_bytes: 4 * 1024 ** 3,
                disk_percent: 41,
                disk_used_bytes: 8 * 1024 ** 3,
                disk_total_bytes: 20 * 1024 ** 3,
                net_rx_bps: 88000,
                net_tx_bps: 12000,
              },
            });
            return;
          }

          if (path === "/api/automatic-tasks") {
            send(res, { tasks: [] });
            return;
          }

          if (path === "/api/extensions") {
            send(res, []);
            return;
          }

          if (path === "/api/settings/preferences") {
            send(res, { language: "zh" });
            return;
          }

          if (path === "/api/devices") {
            send(res, {
              devices: [
                previewDevice("ec25-uk", "EC25-UK", true, true),
                previewDevice("ec25-off", "EC25-OFF", false, false),
              ],
            });
            return;
          }

          if (path === "/api/devices/discovered") {
            send(res, { devices: [] });
            return;
          }

          if (
            path === "/api/upstream-proxies" ||
            path === "/api/upstream-proxy-profile-bindings" ||
            path === "/api/upstream-proxy-countries" ||
            path === "/api/upstream-proxy-country-rules" ||
            path === "/api/sms/contacts" ||
            path === "/api/sms/thread" ||
            path === "/api/logs/history" ||
            path.startsWith("/api/logs/history")
          ) {
            send(res, []);
            return;
          }

          if (path === "/api/settings/notifications") {
            send(res, {});
            return;
          }

          if (path === "/api/settings/https") {
            send(res, { enabled: false });
            return;
          }

          if (path === "/api/settings/developer") {
            send(res, { device_limit: 8, sms_hourly_limit: 0 });
            return;
          }

          if (path === "/api/settings/security") {
            send(res, { mode: "lan", allowed_cidrs: [], trust_proxy_headers: false });
            return;
          }

          if (path === "/api/settings/logging") {
            send(res, { mode: "count", count: 5000, days: 7 });
            return;
          }

          if (path === "/api/system/update/check") {
            send(res, { available: false, version: "1.0.1" });
            return;
          }

          if (method === "GET" && /s$/.test(path.split("/").pop() || "")) {
            send(res, []);
            return;
          }

          send(res, method === "GET" ? {} : { status: "ok" });
        } catch (error) {
          send(res, { error: String(error) }, 500);
        }
      });
    },
  };
}
