import type { LoginResponse, Me } from "./types";
import { clearToken, request, setToken } from "./client";

export async function login(username: string, password: string): Promise<LoginResponse> {
  const result = await request<LoginResponse>("/api/auth/login", {
    method: "POST",
    body: JSON.stringify({ username, password }),
  });
  setToken(result.token);
  return result;
}

export async function register(username: string, password: string): Promise<LoginResponse> {
  const result = await request<LoginResponse>("/api/auth/register", {
    method: "POST",
    body: JSON.stringify({ username, password }),
  });
  setToken(result.token);
  return result;
}

export async function logout(): Promise<void> {
  try {
    await request("/api/auth/logout", { method: "POST" });
  } finally {
    clearToken();
  }
}

export async function me(): Promise<Me> {
  return request<Me>("/api/auth/me");
}
