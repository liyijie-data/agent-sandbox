export type AppView = "chat" | "settings";

export type SettingsTab =
  | "skills"
  | "mcp"
  | "openapi"
  | "network";

export const SETTINGS_TABS: Array<{ id: SettingsTab; label: string }> = [
  { id: "skills", label: "Skills" },
  { id: "mcp", label: "MCP 工具" },
  { id: "openapi", label: "OpenAPI" },
  { id: "network", label: "网络配置" },
];