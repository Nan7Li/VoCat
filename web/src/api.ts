import type {
  ApiErrorBody,
  LoggingSettings,
  LoginResponse,
  SecuritySettings,
  Session,
} from "./types";
import { tl } from "./lib/i18n";

const CSRF_KEY = "vocat.csrf";

// Authenticated pages and same-origin plugin frames share this signal. Clear
// the mutation token immediately so a revoked session cannot leave stale auth
// state behind in the browser.
export function notifyUnauthorized() {
  try {
    sessionStorage.removeItem(CSRF_KEY);
  } catch {
    /* ignore unavailable storage */
  }
  window.dispatchEvent(new Event("vocat:unauthorized"));
}

function isMutation(method: string) {
  return !["GET", "HEAD", "OPTIONS"].includes(method.toUpperCase());
}

function camelizeKey(key: string) {
  return key.replace(/_([a-z0-9])/g, (_, char: string) => char.toUpperCase());
}

function snakeizeKey(key: string) {
  return key
    .replace(/([a-z0-9])([A-Z])/g, "$1_$2")
    .replace(/-/g, "_")
    .toLowerCase();
}

export function camelize<T>(value: unknown): T {
  if (Array.isArray(value)) return value.map((item) => camelize(item)) as T;
  if (value !== null && typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value as Record<string, unknown>).map(([key, item]) => [
        camelizeKey(key),
        camelize(item),
      ]),
    ) as T;
  }
  return value as T;
}

function snakeize(value: unknown): unknown {
  if (Array.isArray(value)) return value.map((item) => snakeize(item));
  if (value !== null && typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value as Record<string, unknown>).map(([key, item]) => [
        snakeizeKey(key),
        snakeize(item),
      ]),
    );
  }
  return value;
}

export class ApiError extends Error {
  status: number;
  code: string;
  requestId: string;
  detail: ApiErrorBody;

  constructor(status: number, detail: ApiErrorBody) {
    super(detail.message || detail.error || `${tl("请求失败")}（HTTP ${status}）`);
    this.name = "ApiError";
    this.status = status;
    this.code = detail.code || "";
    this.requestId = detail.requestId || "";
    this.detail = detail;
  }
}

export interface RequestOptions extends Omit<RequestInit, "body" | "signal"> {
  body?: unknown;
  raw?: boolean;
  signal?: AbortSignal;
  // Default 4s keeps the UI snappy. Update check/apply must opt into a longer
  // window because they wait on GitHub and may download a 20MB+ binary.
  timeoutMs?: number;
}

async function refreshCSRFToken(): Promise<boolean> {
  try {
    const response = await fetch("/api/auth/session", {
      method: "GET",
      headers: { Accept: "application/json" },
      credentials: "include",
      cache: "no-store",
    });
    if (!response.ok) {
      if (response.status === 401) notifyUnauthorized();
      return false;
    }
    const payload = await response.json() as { data?: { csrf_token?: string } };
    const token = payload?.data?.csrf_token;
    if (!token) return false;
    sessionStorage.setItem(CSRF_KEY, token);
    return true;
  } catch {
    return false;
  }
}

async function requestAPI<T>(path: string, options: RequestOptions, retryCSRF: boolean): Promise<T> {
  const { body, raw, timeoutMs, signal, headers: inputHeaders, ...rest } = options;
  const method = (rest.method || "GET").toUpperCase();
  const headers = new Headers(inputHeaders);
  const formBody = typeof FormData !== "undefined" && body instanceof FormData;
  headers.set("Accept", raw ? "*/*" : "application/json");
  if (body !== undefined && !formBody) headers.set("Content-Type", "application/json");
  if (isMutation(method)) {
    const csrf = sessionStorage.getItem(CSRF_KEY);
    if (csrf) headers.set("X-CSRF-Token", csrf);
  }

  const timeout = timeoutMs ?? 4000;
  const response = await fetch(path.startsWith("/api") ? path : `/api${path}`, {
    ...rest,
    method,
    headers,
    credentials: "include",
    signal: signal ?? (timeout > 0 ? AbortSignal.timeout(timeout) : undefined),
    body: body === undefined
      ? undefined
      : formBody
        ? body as FormData
        : JSON.stringify(snakeize(body)),
  });

  if (raw) {
    if (response.status === 401) notifyUnauthorized();
    return response as T;
  }
  const contentType = response.headers.get("content-type") || "";
  const payload = contentType.includes("application/json")
    ? await response.json()
    : { message: await response.text() };
  const normalized = camelize<Record<string, unknown>>(payload);
  if (!response.ok) {
    const nested = normalized.error;
    const detail = nested && typeof nested === "object"
      ? {
          ...(nested as ApiErrorBody),
          requestId: (normalized.requestId as string | undefined) || (nested as ApiErrorBody).requestId,
        }
      : normalized as ApiErrorBody;
    if (
      retryCSRF &&
      isMutation(method) &&
      response.status === 403 &&
      detail.code === "invalid_csrf"
    ) {
      if (await refreshCSRFToken()) return requestAPI<T>(path, options, false);
      notifyUnauthorized();
    } else if (response.status === 401) {
      notifyUnauthorized();
    }
    throw new ApiError(response.status, detail);
  }
  return (Object.prototype.hasOwnProperty.call(normalized, "data") ? normalized.data : normalized) as T;
}

export async function api<T>(path: string, options: RequestOptions = {}): Promise<T> {
  return requestAPI<T>(path, options, true);
}

export async function login(username: string, password: string) {
  const result = await api<LoginResponse & { user?: { username?: string } }>("/auth/login", {
    method: "POST",
    body: { username, password },
  });
  if (result.csrfToken) sessionStorage.setItem(CSRF_KEY, result.csrfToken);
  return result;
}

export async function session() {
  const result = await api<Session & { user?: { username?: string } }>("/auth/session");
  if (result.csrfToken) sessionStorage.setItem(CSRF_KEY, result.csrfToken);
  return {
    ...result,
    username: result.username || result.user?.username || "",
    role: result.role || "Administrator",
  };
}

export async function logout() {
  try {
    await api("/auth/logout", { method: "POST" });
  } finally {
    sessionStorage.removeItem(CSRF_KEY);
  }
}

export function getSecuritySettings() {
  return api<SecuritySettings>("/settings/security");
}

export function updateSecuritySettings(settings: {
  mode: SecuritySettings["mode"];
  allowedCidrs: string[];
  trustProxyHeaders: boolean;
}) {
  return api<SecuritySettings>("/settings/security", { method: "PUT", body: settings });
}

export function getLoggingSettings() {
  return api<LoggingSettings>("/settings/logging");
}

export function updateLoggingSettings(settings: {
  mode: LoggingSettings["mode"];
  count: number;
  days: number;
}) {
  return api<LoggingSettings>("/settings/logging", { method: "PUT", body: settings });
}

export function apiMessage(error: unknown) {
  if (error instanceof ApiError) {
    const suffix = error.requestId ? `（${tl("请求")} ${error.requestId}）` : "";
    return `${error.message}${suffix}`;
  }
  if (error instanceof DOMException && (error.name === "TimeoutError" || error.name === "AbortError")) {
    return tl("请求超时，请稍后重试");
  }
  if (error instanceof Error) {
    const text = error.message || "";
    if (/timeout|aborted/i.test(text)) return tl("请求超时，请稍后重试");
    return text;
  }
  return tl("请求未完成，检查服务状态后重试");
}

export function eventStreamURL(path: string, params?: URLSearchParams) {
  const suffix = params?.toString();
  return `${path.startsWith("/api") ? path : `/api${path}`}${suffix ? `?${suffix}` : ""}`;
}
