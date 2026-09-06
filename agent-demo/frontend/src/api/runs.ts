import type {
  Artifact,
  CreateRunPayload,
  DownloadUrl,
  Run,
  RunSummary,
  SteerReceipt,
} from "./types";
import { redactBody, request } from "./client";

export interface SseEvent {
  event: string;
  data: string;
  id: string;
}

export interface RunStreamHandlers {
  onRunId: (runId: string) => void;
  onEvent: (event: SseEvent) => void;
}

async function consumeSseStream(
  response: Response,
  handlers: RunStreamHandlers,
): Promise<void> {
  if (!response.body) throw new Error("streaming response missing body");
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  let eventName = "message";
  let eventId = "";
  const data: string[] = [];

  const dispatch = () => {
    if (data.length) {
      const eventData = data.join("\n");
      console.log(
        `[sse] event=${eventName} id=${eventId} data=${redactBody(eventData)}`,
      );
      handlers.onEvent({ event: eventName, data: eventData, id: eventId });
    }
    eventName = "message";
    eventId = "";
    data.length = 0;
  };

  for (;;) {
    const next = await reader.read();
    buffer += decoder.decode(next.value || new Uint8Array(), {
      stream: !next.done,
    });
    let index: number;
    while ((index = buffer.indexOf("\n")) >= 0) {
      const line = buffer.slice(0, index).replace(/\r$/, "");
      buffer = buffer.slice(index + 1);
      if (!line) {
        dispatch();
        continue;
      }
      if (line.startsWith("id: ")) eventId = line.slice(4);
      else if (line.startsWith("event: ")) eventName = line.slice(7);
      else if (line.startsWith("data: ")) data.push(line.slice(6));
    }
    if (next.done) {
      dispatch();
      return;
    }
  }
}

export async function streamRun(
  payload: CreateRunPayload,
  handlers: RunStreamHandlers,
): Promise<void> {
  const headers = new Headers({
    "Content-Type": "application/json",
    Accept: "text/event-stream",
  });
  const token = localStorage.getItem("agent-demo-token");
  if (token) headers.set("Authorization", `Bearer ${token}`);

  const started = performance.now();
  console.log(`[sse] POST /api/runs body=${redactBody(JSON.stringify(payload))}`);
  let response: Response;
  try {
    response = await fetch("/api/runs", {
      method: "POST",
      headers,
      body: JSON.stringify(payload),
    });
  } catch (err) {
    console.error(
      `[sse] POST /api/runs error=${err instanceof Error ? err.message : String(err)} (${Math.round(performance.now() - started)}ms)`,
    );
    throw new Error(err instanceof Error ? err.message : String(err));
  }
  if (!response.ok) {
    const raw = await response.text();
    let body: { detail?: string; error?: string } = {};
    try {
      body = JSON.parse(raw) as { detail?: string; error?: string };
    } catch {
    }
    console.error(
      `[sse] POST /api/runs -> ${response.status} (${Math.round(performance.now() - started)}ms) body=${redactBody(raw || "{}")}`,
    );
    throw new Error(body.detail || body.error || response.statusText);
  }

  const runId = response.headers.get("X-Demo-Run-ID");
  if (!runId) throw new Error("streaming Run response missing run ID");
  handlers.onRunId(runId);
  console.log(
    `[sse] POST /api/runs connected status=${response.status} runId=${runId} (${Math.round(performance.now() - started)}ms)`,
  );

  await consumeSseStream(response, handlers);
}

export async function streamRunEvents(
  runId: string,
  handlers: RunStreamHandlers,
  lastEventId?: string,
): Promise<void> {
  const headers = new Headers({
    Accept: "text/event-stream",
  });
  const token = localStorage.getItem("agent-demo-token");
  if (token) headers.set("Authorization", `Bearer ${token}`);
  if (lastEventId) headers.set("Last-Event-ID", lastEventId);

  const url = lastEventId
    ? `/api/runs/${runId}/events?last_event_id=${encodeURIComponent(lastEventId)}`
    : `/api/runs/${runId}/events`;

  const started = performance.now();
  console.log(`[sse] GET ${url}${lastEventId ? ` lastEventId=${lastEventId}` : ""}`);
  let response: Response;
  try {
    response = await fetch(url, { method: "GET", headers });
  } catch (err) {
    console.error(
      `[sse] GET ${url} error=${err instanceof Error ? err.message : String(err)} (${Math.round(performance.now() - started)}ms)`,
    );
    throw new Error(err instanceof Error ? err.message : String(err));
  }
  if (!response.ok) {
    const raw = await response.text();
    let body: { detail?: string; error?: string } = {};
    try {
      body = JSON.parse(raw) as { detail?: string; error?: string };
    } catch {
    }
    console.error(
      `[sse] GET ${url} -> ${response.status} (${Math.round(performance.now() - started)}ms) body=${redactBody(raw || "{}")}`,
    );
    throw new Error(body.detail || body.error || response.statusText);
  }

  handlers.onRunId(runId);
  console.log(
    `[sse] GET ${url} connected status=${response.status} (${Math.round(performance.now() - started)}ms)`,
  );
  await consumeSseStream(response, handlers);
}

export function getRun(runId: string): Promise<Run> {
  return request<Run>(`/api/runs/${runId}`);
}

export function listConversationRuns(
  conversationId: string,
): Promise<RunSummary[]> {
  return request<RunSummary[]>(`/api/conversations/${conversationId}/runs`);
}

export function cancelRun(runId: string): Promise<unknown> {
  return request(`/api/runs/${runId}/cancel`, { method: "POST" });
}

export interface AnswerPayload {
  answer: unknown;
  access_refresh?: string[];
}

export function answerRun(
  runId: string,
  inputId: string,
  payload: AnswerPayload,
): Promise<unknown> {
  return request(`/api/runs/${runId}/inputs/${inputId}/answer`, {
    method: "POST",
    body: JSON.stringify(payload),
  });
}

export function listArtifacts(runId: string): Promise<Artifact[]> {
  return request<Artifact[]>(`/api/runs/${runId}/artifacts`);
}

export function artifactDownloadUrl(artifactId: string): Promise<DownloadUrl> {
  return request<DownloadUrl>(`/api/artifacts/${artifactId}/download-url`);
}

export interface SteerOptions {
  fileIds?: string[];
  skillIds?: string[];
}

export function createSteer(
  runId: string,
  steerId: string,
  message: string,
  opts?: SteerOptions,
): Promise<SteerReceipt> {
  return request<SteerReceipt>(`/api/runs/${runId}/steers`, {
    method: "POST",
    body: JSON.stringify({
      steer_id: steerId,
      message,
      file_ids: opts?.fileIds ?? [],
      skill_ids: opts?.skillIds ?? [],
    }),
  });
}

export function getSteer(
  runId: string,
  steerId: string,
): Promise<SteerReceipt> {
  return request<SteerReceipt>(`/api/runs/${runId}/steers/${steerId}`);
}
