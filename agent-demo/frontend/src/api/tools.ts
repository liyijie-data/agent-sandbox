import type { CreateToolPayload, McpDiscoveredTool, Tool } from "./types";
import { request } from "./client";

export function listTools(): Promise<Tool[]> {
  return request<Tool[]>("/api/tools");
}

export function createTool(payload: CreateToolPayload): Promise<Tool> {
  return request<Tool>("/api/tools", {
    method: "POST",
    body: JSON.stringify(payload),
  });
}

export function discoverMcpTools(endpoint: string): Promise<McpDiscoveredTool[]> {
  return request<{ tools: McpDiscoveredTool[] }>("/api/tools/mcp-discover", {
    method: "POST",
    body: JSON.stringify({ endpoint }),
  }).then((value) => value.tools);
}

export function setToolEnabled(toolId: string, enabled: boolean): Promise<Tool> {
  return request<Tool>(`/api/tools/${toolId}/enable`, {
    method: "POST",
    body: JSON.stringify({ enabled }),
  });
}

export function deleteTool(toolId: string): Promise<unknown> {
  return request(`/api/tools/${toolId}`, { method: "DELETE" });
}