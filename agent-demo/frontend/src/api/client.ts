const TOKEN_KEY = "agent-demo-token";

export const getToken = (): string | null => localStorage.getItem(TOKEN_KEY);
export const setToken = (token: string): void => localStorage.setItem(TOKEN_KEY, token);
export const clearToken = (): void => localStorage.removeItem(TOKEN_KEY);

type ErrorBody = { detail?: string; error?: string };

export class ApiError extends Error {
  status: number;
  constructor(message: string, status = 0) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

const TRUNCATE_LIMIT = 4000;

const REDACTED_FIELDS = new Set([
  "authorization",
  "password",
  "token",
  "secret",
  "api_key",
  "client_api_key",
]);

const SIGNED_URL_FIELDS = new Set(["upload_url", "download_url"]);
const SIGNED_QUERY_PATTERN = /signature|credential|securitytoken|secret|accesskey/i;

export function truncate(text: string): string {
  return text.length > TRUNCATE_LIMIT
    ? `${text.slice(0, TRUNCATE_LIMIT)}...(len=${text.length})`
    : text;
}

export function maskSignedUrl(url: string): string {
  const idx = url.indexOf("?");
  if (idx < 0) return url;
  const base = url.slice(0, idx);
  const query = url
    .slice(idx + 1)
    .split("&")
    .map((part) => {
      const eq = part.indexOf("=");
      const key = eq < 0 ? part : part.slice(0, eq);
      if (SIGNED_QUERY_PATTERN.test(key)) {
        const value = eq < 0 ? "" : part.slice(eq + 1);
        return `${key}=${value.slice(0, 64)}...(len=${value.length})`;
      }
      return part;
    })
    .join("&");
  return `${base}?${query}`;
}

function redactValue(value: unknown, field?: string): unknown {
  if (Array.isArray(value)) return value.map((item) => redactValue(item, field));
  if (value && typeof value === "object") {
    const out: Record<string, unknown> = {};
    for (const [key, item] of Object.entries(value as Record<string, unknown>)) {
      out[key] = redactValue(item, key);
    }
    return out;
  }
  if (typeof value === "string") {
    if (field && REDACTED_FIELDS.has(field)) return "[REDACTED]";
    if (value.startsWith("Bearer ") || value.startsWith("bearer ")) return "[REDACTED]";
    if (field && SIGNED_URL_FIELDS.has(field)) return maskSignedUrl(value);
  }
  return value;
}

export function redactBody(text: string): string {
  try {
    return truncate(JSON.stringify(redactValue(JSON.parse(text))));
  } catch {
    return truncate(text);
  }
}

function describeBody(init: RequestInit): string {
  const body = init.body;
  if (body == null) return "-";
  const contentType =
    init.headers instanceof Headers ? (init.headers.get("Content-Type") ?? "") : "";
  const ct = contentType.toLowerCase();
  if (typeof body === "string") {
    if (ct.includes("json") || ct.includes("text")) return redactBody(body);
    return `<binary> ct=${ct || "?"} len=${body.length}`;
  }
  if (body instanceof Blob) {
    return `<binary> ct=${ct || "?"} len=${body.size}`;
  }
  if (body instanceof FormData || body instanceof ArrayBuffer) {
    return `<binary> ct=${ct || "?"}`;
  }
  return "<binary>";
}

function describeResponseText(text: string, contentType: string | null): string {
  if (!text) return "-";
  const ct = (contentType ?? "").toLowerCase();
  if (ct.includes("json") || ct.includes("text")) return redactBody(text);
  return `<binary> ct=${ct || "?"} len=${text.length}`;
}

export async function request<T>(
  path: string,
  init: RequestInit = {},
): Promise<T> {
  const method = (init.method ?? "GET").toUpperCase();
  const started = performance.now();
  console.log(`[api] ${method} ${path} body=${describeBody(init)}`);

  const merged = new Headers(init.headers);
  if (!merged.has("Content-Type") && init.body) {
    merged.set("Content-Type", "application/json");
  }
  const token = getToken();
  if (token) merged.set("Authorization", `Bearer ${token}`);

  let response: Response;
  try {
    response = await fetch(path, { ...init, headers: merged });
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    console.error(
      `[api] ${method} ${path} error=${message} (${Math.round(performance.now() - started)}ms)`,
    );
    throw new ApiError(message);
  }

  const text = await response.text();
  const duration = Math.round(performance.now() - started);

  if (!response.ok) {
    let body: ErrorBody = {};
    try {
      body = JSON.parse(text) as ErrorBody;
    } catch {
    }
    console.error(
      `[api] ${method} ${path} -> ${response.status} (${duration}ms) body=${describeResponseText(
        text,
        response.headers.get("Content-Type"),
      )}`,
    );
    throw new ApiError(body.detail || body.error || response.statusText, response.status);
  }

  console.log(
    `[api] ${method} ${path} -> ${response.status} (${duration}ms) body=${describeResponseText(
      text,
      response.headers.get("Content-Type"),
    )}`,
  );
  return JSON.parse(text) as T;
}
