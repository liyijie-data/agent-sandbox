export interface LoginResponse {
  token: string;
  username: string;
}

export interface Me {
  id: string;
  username: string;
}

export interface Conversation {
  id: string;
  title: string;
  user_id?: string;
  created_at?: string;
  updated_at?: string;
}

export type Role = "user" | "assistant" | "system";

export type AttachmentKind = "file" | "skill" | "openapi" | "tool";

export interface AttachmentMeta {
  id: string;
  name: string;
  kind: AttachmentKind;
}

export interface Message {
  id: string;
  role: Role;
  content: string;
  run_id?: string | null;
  reasoning_content?: string | null;
  attachments_json?: unknown;
  seq?: number;
  created_at?: string;
}

export type ResourceKind = "files" | "skills" | "openapi";

export interface Resource {
  id: string;
  name: string;
  version?: string | null;
  sha256: string;
  size_bytes: number;
  status: string;
  created_at?: string;
}

export type ToolType = "openapi" | "mcp";

export interface Tool {
  id: string;
  type: ToolType;
  name: string;
  endpoint: string;
  spec_resource_id?: string | null;
  allowed_operations?: string[];
  enabled?: boolean;
  created_at?: string;
}

export interface CreateToolPayload {
  type: ToolType;
  name: string;
  endpoint: string;
  spec_resource_id?: string;
  allowed_operations?: string[];
  auth?: Record<string, unknown>;
}

export interface McpDiscoveredTool {
  name: string;
  description?: string;
}

export type RunStatus =
  | "creating"
  | "queued"
  | "preparing"
  | "running"
  | "awaiting_input"
  | "cancel_requested"
  | "succeeded"
  | "failed"
  | "cancelled"
  | "expired";

export type SteerStatus =
  | "pending"
  | "incorporated"
  | "not_applied"
  | "unknown";

export interface SteerReceipt {
  run_id: string;
  steer_id: string;
  seq: number;
  status: SteerStatus;
  reason_code?: string | null;
  accepted_at: string;
  incorporated_at?: string | null;
  stage?: number;
}

export type HumanInputKind = "question" | "choice" | "approval";

export interface HumanInputOption {
  value: string;
  label: string;
}

export interface HumanInputRequest {
  run_id: string;
  input_id: string;
  kind: HumanInputKind;
  prompt: string;
  options?: HumanInputOption[];
  expires_at: string;
}

export interface Run {
  id: string;
  platform_run_id: string;
  req_id: string;
  trace_id: string;
  status: RunStatus;
  result?: unknown;
  pending_input?: HumanInputRequest | null;
  queue_position?: number | null;
  queue_length?: number | null;
  wait_reason?: string | null;
}

export interface RunSummary {
  id: string;
  status: RunStatus;
  last_event_id?: string | null;
  created_at: string;
}

export interface QueueProgress {
  run_id: string;
  queue_position: number;
  queue_length: number;
  wait_reason?: string;
}

export interface ToolCallStatus {
  tool_call_id: string;
  tool_name: string;
  status: "started" | "succeeded" | "failed";
  error_type?: string;
  tool_id?: string;
  operation?: string;
  model_tool_name?: string;
  display_tool_name?: string;
  arguments?: unknown;
  result?: unknown;
  duration_ms?: number;
  details_truncated?: boolean;
}

export type StreamTimelineItem =
  | {
      kind: "reasoning" | "content";
      id: string;
      content: string;
    }
  | {
      kind: "tool";
      id: string;
      tool_call_id: string;
      tool_name: string;
      status: ToolCallStatus["status"];
      error_type?: string;
      input?: unknown;
      result?: unknown;
      tool_id?: string;
      operation?: string;
      model_tool_name?: string;
      display_tool_name?: string;
      duration_ms?: number;
      details_truncated?: boolean;
    }
  | {
      kind: "input_request";
      id: string;
      prompt: string;
      input_id: string;
      input_kind: HumanInputKind;
      options?: HumanInputOption[];
    };

export interface Artifact {
  id: string;
  name: string;
  content_type?: string;
  sha256: string;
  size_bytes: number;
  status: string;
  created_at?: string;
}

export interface DownloadUrl {
  download_url: string;
  expires_in: number;
}

export interface CreateRunPayload {
  conversation_id: string;
  prompt: string;
  file_ids: string[];
  skill_ids: string[];
  tool_ids: string[];
	reasoning_effort?: string;
	context_window_tokens?: number;
	max_output_tokens?: number;
	model_parameters?: Record<string, unknown>;
}

export interface RunTerminalEvent {
  run_id: string;
  status: "succeeded" | "failed" | "cancelled" | "expired";
  result?: unknown;
}

export interface HostAlias {
  hostname: string;
  ip: string;
}

export interface EgressPort {
  protocol: string;
  port: number;
}

export interface EgressRule {
  cidr: string;
  ports: EgressPort[];
}

export interface NetworkPolicy {
  egress: EgressRule[];
}

export interface NetworkConfigResponse {
  scope: string;
  source: string;
  revision_id?: string;
  revision: number;
  rollout_status: string;
  host_aliases: HostAlias[];
  network_policy: NetworkPolicy;
}

export interface NetworkConfigRequest {
  req_id: string;
  expected_active_revision?: string;
  host_aliases: HostAlias[];
  network_policy: NetworkPolicy;
}
