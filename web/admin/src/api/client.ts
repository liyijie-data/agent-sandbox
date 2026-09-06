import { PlatformError } from "./types";

const API_BASE = "/console/api/v1";

interface RequestOptions {
  method?: string;
  body?: unknown;
  params?: Record<string, string | number>;
}

export async function api<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const url = new URL(`${API_BASE}${path}`, window.location.origin);
  if (options.params) {
    for (const [k, v] of Object.entries(options.params)) url.searchParams.set(k, String(v));
  }
  const response = await fetch(url.toString(), {
    method: options.method ?? "GET",
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
  });
  if (!response.ok) {
    let code = "error";
    let message = `HTTP ${response.status}`;
    try {
      const detail = await response.json();
      if (detail?.error?.code) {
        code = detail.error.code;
        message = detail.error.message ?? message;
      }
    } catch {
    }
    throw new PlatformError(response.status, code, message);
  }
  return (await response.json()) as T;
}

export async function login(token: string): Promise<void> {
  const response = await fetch("/console/api/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    body: JSON.stringify({ token }),
  });
  if (response.status === 401) {
    throw new Error("SERVICE_TOKEN 无效（平台返回 401）");
  }
  if (!response.ok) {
    throw new Error(`登录失败（HTTP ${response.status}）`);
  }
}

export async function logout(): Promise<void> {
  await fetch("/console/api/logout", { method: "POST", credentials: "same-origin" });
}

export async function me(): Promise<boolean> {
  const response = await fetch("/console/api/me", { credentials: "same-origin" });
  return response.ok;
}
