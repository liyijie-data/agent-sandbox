import { useCallback, useEffect, useRef, useState } from "react";
import {
  answerRun,
  cancelRun,
  createSteer,
  getRun,
  getSteer,
  listConversationRuns,
  streamRun,
  streamRunEvents,
} from "../api/runs";
import type { SseEvent } from "../api/runs";
import type {
  HumanInputRequest,
  QueueProgress,
  RunStatus,
  StreamTimelineItem,
  SteerReceipt,
  SteerStatus,
  ToolCallStatus,
} from "../api/types";
import type {
  SendInput,
  UseRunStreamOptions,
} from "./hooks";

interface PendingState {
  request: HumanInputRequest;
  status: RunStatus;
}

function parseQueueProgress(data: string, fallbackRunId: string): QueueProgress | null {
  try {
    const parsed = JSON.parse(data) as Record<string, unknown>;
    const position = Number(parsed.queue_position);
    const length = Number(parsed.queue_length);
    if (!Number.isFinite(position) || !Number.isFinite(length)) return null;
    return {
      run_id: String(parsed.run_id || fallbackRunId),
      queue_position: position,
      queue_length: length,
      wait_reason: typeof parsed.wait_reason === "string" ? parsed.wait_reason : undefined,
    };
  } catch {
    return null;
  }
}

function takeCodePoints(value: string, count: number): [string, string] {
  const points = Array.from(value);
  return [points.slice(0, count).join(""), points.slice(count).join("")];
}

const STEER_EVENTS: Record<string, SteerStatus> = {
  "steer.accepted": "pending",
  "steer.incorporated": "incorporated",
  "steer.not_applied": "not_applied",
  "steer.unknown": "unknown",
};

function parseSteerEvent(
  eventName: string,
  data: string,
  fallbackRunId: string,
): SteerReceipt | null {
  const status = STEER_EVENTS[eventName];
  if (!status) return null;
  try {
    const parsed = JSON.parse(data) as Record<string, unknown>;
    return {
      run_id: String(parsed.run_id || fallbackRunId),
      steer_id: String(parsed.steer_id || ""),
      seq: Number(parsed.seq) || 0,
      status,
      reason_code:
        typeof parsed.reason_code === "string" ? parsed.reason_code : null,
      accepted_at: String(parsed.accepted_at || ""),
      incorporated_at:
        typeof parsed.incorporated_at === "string"
          ? parsed.incorporated_at
          : null,
      stage: typeof parsed.stage === "number" ? parsed.stage : undefined,
    };
  } catch {
    return null;
  }
}

const STREAM_END_ERROR_CODES = new Set(["events_expired", "event_stream_unavailable"]);

const RESUMABLE_STATUSES: ReadonlySet<string> = new Set([
  "creating",
  "running",
  "preparing",
  "queued",
  "awaiting_input",
]);

export function useRunStream(options: UseRunStreamOptions) {
  const { getActive, createConversation, setMessages, refreshThread, clearResources } =
    options;
  const [runId, setRunId] = useState<string | null>(null);
  const [runStatus, setRunStatus] = useState<RunStatus | null>(null);
  const [pending, setPending] = useState<PendingState | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [lastEventId, setLastEventId] = useState<string | null>(null);
  const [steerReceipts, setSteerReceipts] = useState<SteerReceipt[]>([]);
  const [queueProgress, setQueueProgress] = useState<QueueProgress | null>(null);
  const [timeline, setTimeline] = useState<StreamTimelineItem[]>([]);
  const [timelineMessageId, setTimelineMessageId] = useState<string | null>(null);
  const queueClosedRef = useRef(false);
  const inFlightRef = useRef(false);
  const contentBufferRef = useRef("");
  const reasoningBufferRef = useRef("");
  const outputTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const finalizeRunIdRef = useRef<string | null>(null);
  const outputStartedRef = useRef(false);

  const appendDelta = useCallback(
    (content?: unknown, reasoning?: unknown) => {
      if (typeof content !== "string" && typeof reasoning !== "string") return;
      setMessages((x) => {
        const y = x.map((m) => ({ ...m }));
        const last = y[y.length - 1];
        if (last && last.role === "assistant") {
          if (typeof content === "string")
            last.content = (last.content || "") + content;
          if (typeof reasoning === "string")
            last.reasoning_content = (last.reasoning_content || "") + reasoning;
        }
        return y;
      });
    },
    [setMessages],
  );

  const appendTimelineText = useCallback(
    (kind: "reasoning" | "content", content: string) => {
      if (!content) return;
      setTimeline((prev) => {
        const last = prev[prev.length - 1];
        if (last?.kind === kind) {
          return [...prev.slice(0, -1), { ...last, content: last.content + content }];
        }
        return [...prev, { kind, id: `${kind}-${Date.now()}-${prev.length}`, content }];
      });
    },
    [],
  );

  const handleSteerEvent = useCallback(
    (eventName: string, data: string, activeRunId: string) => {
      const receipt = parseSteerEvent(eventName, data, activeRunId);
      if (!receipt || !receipt.steer_id) return;
      setSteerReceipts((prev) => {
        const idx = prev.findIndex((r) => r.steer_id === receipt.steer_id);
        if (idx >= 0) {
          const next = [...prev];
          next[idx] = receipt;
          return next;
        }
        return [...prev, receipt];
      });
      if (receipt.status === "not_applied") {
        setError(receipt.reason_code ? `引导未应用：${receipt.reason_code}` : "引导未应用");
      }
    },
    [],
  );

  const finalize = useCallback(
    async (localRunId: string) => {
      setError(null);
      try {
        const value = await getRun(localRunId);
        setRunId(localRunId);
        setRunStatus(value.status);
        if (
          value.status === "queued" &&
          value.queue_position != null &&
          !outputStartedRef.current &&
          !queueClosedRef.current
        ) {
          setQueueProgress({
            run_id: value.platform_run_id || localRunId,
            queue_position: value.queue_position,
            queue_length: value.queue_length ?? 0,
            wait_reason: value.wait_reason ?? undefined,
          });
        } else {
          setQueueProgress(null);
        }
        if (value.status === "awaiting_input") {
          const request = value.pending_input;
          if (request?.input_id) {
            setPending({ request, status: "awaiting_input" });
            return;
          }
          const legacyRequest = (value.result as { request?: HumanInputRequest } | null)
            ?.request;
          if (legacyRequest?.input_id) {
            setPending({ request: legacyRequest, status: "awaiting_input" });
            return;
          }
        }
        setPending(null);
        await refreshThread();
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
      }
    },
    [refreshThread],
  );

  const flushOutput = useCallback(() => {
    outputTimerRef.current = null;
    const backlog =
      Array.from(contentBufferRef.current).length +
      Array.from(reasoningBufferRef.current).length;
    const step = Math.max(1, backlog);
    const [content, remainingContent] = takeCodePoints(contentBufferRef.current, step);
    const [reasoning, remainingReasoning] = takeCodePoints(
      reasoningBufferRef.current,
      step,
    );
    contentBufferRef.current = remainingContent;
    reasoningBufferRef.current = remainingReasoning;
    if (content || reasoning) appendDelta(content, reasoning);

    if (contentBufferRef.current || reasoningBufferRef.current) {
      outputTimerRef.current = setTimeout(flushOutput, 0);
    } else if (finalizeRunIdRef.current) {
      const pendingRunId = finalizeRunIdRef.current;
      finalizeRunIdRef.current = null;
      void finalize(pendingRunId);
    }
  }, [appendDelta, finalize]);

  const scheduleOutputFlush = useCallback(() => {
    if (outputTimerRef.current == null) {
      outputTimerRef.current = setTimeout(flushOutput, 0);
    }
  }, [flushOutput]);

  const requestFinalize = useCallback(
    (activeRunId: string) => {
      finalizeRunIdRef.current = activeRunId;
      if (!contentBufferRef.current && !reasoningBufferRef.current) {
        const pendingRunId = finalizeRunIdRef.current;
        finalizeRunIdRef.current = null;
        void finalize(pendingRunId);
      } else {
        scheduleOutputFlush();
      }
    },
    [finalize, scheduleOutputFlush],
  );

  const clearOutputBuffer = useCallback(() => {
    if (outputTimerRef.current != null) {
      clearTimeout(outputTimerRef.current);
      outputTimerRef.current = null;
    }
    contentBufferRef.current = "";
    reasoningBufferRef.current = "";
    finalizeRunIdRef.current = null;
    outputStartedRef.current = false;
  }, []);

  const handleEvent = useCallback(
    (ev: SseEvent, activeRunId: string) => {
      if (ev.id) {
        setLastEventId(ev.id);
      }
      if (STEER_EVENTS[ev.event]) {
        handleSteerEvent(ev.event, ev.data, activeRunId);
        return;
      }
      if (ev.event === "run.queue_progress") {
        const qp = parseQueueProgress(ev.data, activeRunId);
        if (qp && !outputStartedRef.current && !queueClosedRef.current) {
          setQueueProgress(qp);
          setRunStatus("queued");
        }
        return;
      }
      if (ev.event === "run.started") {
        queueClosedRef.current = true;
        setQueueProgress(null);
        setRunStatus("running");
        return;
      }
      if (ev.event === "run.preparing") {
        queueClosedRef.current = true;
        setQueueProgress(null);
        setRunStatus("preparing");
        return;
      }
      if (ev.event === "agent.tool_call" || ev.event === "agent.tool_result") {
        try {
          const parsed = JSON.parse(ev.data) as Partial<ToolCallStatus> & Record<string, unknown>;
          if (!parsed.tool_call_id || !parsed.tool_name) return;
          const status = parsed.status as ToolCallStatus["status"];
          if (status !== "started" && status !== "succeeded" && status !== "failed") return;
          const toolCallId = String(parsed.tool_call_id);
          const input = ev.event === "agent.tool_call"
            ? parsed.arguments ?? parsed.input ?? parsed.parameters
            : undefined;
          const result = ev.event === "agent.tool_result"
            ? parsed.result ?? parsed.output
            : undefined;
          setTimeline((prev) => {
            const idx = prev.findIndex(
              (item) => item.kind === "tool" && item.tool_call_id === toolCallId,
            );
            const item: Extract<StreamTimelineItem, { kind: "tool" }> = {
              kind: "tool",
              id: `tool-${toolCallId}`,
              tool_call_id: toolCallId,
              tool_name: String(parsed.tool_name),
              status,
              ...(typeof parsed.error_type === "string" ? { error_type: parsed.error_type } : {}),
              ...(typeof parsed.tool_id === "string" ? { tool_id: parsed.tool_id } : {}),
              ...(typeof parsed.operation === "string" ? { operation: parsed.operation } : {}),
              ...(typeof parsed.model_tool_name === "string" ? { model_tool_name: parsed.model_tool_name } : {}),
              ...(typeof parsed.display_tool_name === "string" ? { display_tool_name: parsed.display_tool_name } : {}),
              ...(typeof parsed.duration_ms === "number" ? { duration_ms: parsed.duration_ms } : {}),
              ...(parsed.details_truncated === true ? { details_truncated: true } : {}),
              ...(input !== undefined ? { input } : {}),
              ...(result !== undefined ? { result } : {}),
            };
            if (idx < 0) return [...prev, item];
            const current = prev[idx];
            if (current.kind !== "tool") return prev;
            const next = [...prev];
            next[idx] = {
              ...current,
              ...item,
              status: current.status !== "started" && status === "started" ? current.status : status,
              ...(current.input !== undefined && input === undefined ? { input: current.input } : {}),
              ...(current.result !== undefined && result === undefined ? { result: current.result } : {}),
            };
            return next;
          });
        } catch { }
        return;
      }
      if (ev.event === "agent.request_input") {
        let request: HumanInputRequest;
        try {
          const parsed = JSON.parse(ev.data);
          request = {
            run_id: String(parsed.run_id || activeRunId),
            input_id: String(parsed.input_id || ""),
            kind: (parsed.kind as HumanInputRequest["kind"]) || "question",
            prompt: String(parsed.prompt || ""),
            options: Array.isArray(parsed.options) ? parsed.options : [],
            expires_at: String(parsed.expires_at || ""),
          };
        } catch {
          return;
        }
        if (request.input_id) {
          setTimeline((prev) => [
            ...prev,
            {
              kind: "input_request",
              id: `input-request-${request.input_id}`,
              prompt: request.prompt,
              input_id: request.input_id,
              input_kind: request.kind,
              options: request.options,
            },
          ]);
          setPending({ request, status: "awaiting_input" });
          setRunStatus("awaiting_input");
        }
        return;
      }
      if (ev.data === "[DONE]") {
        if (activeRunId) requestFinalize(activeRunId);
        return;
      }
      let chunk: {
        error?: unknown;
        choices?: Array<{ delta?: { content?: unknown; reasoning_content?: unknown } }>;
      };
      try {
        chunk = JSON.parse(ev.data);
      } catch {
        return;
      }
      if (chunk.error) {
        const code =
          typeof chunk.error === "object" && chunk.error !== null
            ? String((chunk.error as { code?: unknown }).code ?? "")
            : "";
        if (STREAM_END_ERROR_CODES.has(code) && activeRunId) {
          requestFinalize(activeRunId);
          return;
        }
        const detail = code === "context_limit_exceeded"
          ? "上下文长度超限，压缩后仍无法继续。请减少输入内容或新建会话后重试。"
          : "执行失败";
        setError(detail);
        return;
      }
      const delta = chunk.choices?.[0]?.delta;
      const content = delta?.content;
      const reasoning = delta?.reasoning_content;
      if (typeof content === "string" || typeof reasoning === "string") {
        if (!outputStartedRef.current) {
          outputStartedRef.current = true;
          queueClosedRef.current = true;
          setQueueProgress(null);
          setRunStatus("running");
        }
        if (typeof content === "string") contentBufferRef.current += content;
        if (typeof reasoning === "string") reasoningBufferRef.current += reasoning;
        if (typeof content === "string") appendTimelineText("content", content);
        if (typeof reasoning === "string") appendTimelineText("reasoning", reasoning);
        scheduleOutputFlush();
      }
    },
    [appendTimelineText, handleSteerEvent, requestFinalize, scheduleOutputFlush],
  );

  const send = useCallback(
    async (input: SendInput) => {
      if (inFlightRef.current) return;
      inFlightRef.current = true;
      setError(null);
      setPending(null);
      setLastEventId(null);
      setSteerReceipts([]);
      setTimeline([]);
      setQueueProgress(null);
      queueClosedRef.current = false;
      clearOutputBuffer();
      const attachments = input.attachments.map((item) => ({ ...item }));
      let localRunId = "";
      try {
        let conversation = getActive();
        if (!conversation) {
          conversation = await createConversation(input.prompt.slice(0, 32) || "新会话");
        }

        setRunId(null);
        setRunStatus("creating");
        const assistantMessageId = `local-assistant-${Date.now()}`;
        setMessages((x) => [
          ...x,
          {
            id: `local-user-${Date.now()}`,
            role: "user",
            content: input.prompt,
            attachments_json: attachments,
          },
          {
            id: assistantMessageId,
            role: "assistant",
            content: "",
          },
        ]);
        setTimelineMessageId(assistantMessageId);
        clearResources();

        await streamRun(
          {
            conversation_id: conversation.id,
            prompt: input.prompt,
            file_ids: input.fileIds,
            skill_ids: input.skillIds,
            tool_ids: input.toolIds,
            reasoning_effort: input.reasoningEffort,
            context_window_tokens: input.contextWindowTokens,
            max_output_tokens: input.maxOutputTokens,
            model_parameters: input.modelParameters,
          },
          {
            onRunId: (id) => {
              localRunId = id;
              setRunId(id);
              setRunStatus("queued");
            },
            onEvent: (ev) => handleEvent(ev, localRunId),
          },
        );
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
        if (localRunId) {
          requestFinalize(localRunId);
        }
      } finally {
        inFlightRef.current = false;
      }
    },
    [
      clearResources,
      clearOutputBuffer,
      createConversation,
      finalize,
      getActive,
      handleEvent,
      requestFinalize,
      setMessages,
    ],
  );

  const resume = useCallback(
    async (resumeRunId: string, resumeLastEventId?: string) => {
      if (inFlightRef.current) return;
      inFlightRef.current = true;
      setError(null);
      setPending(null);
      setSteerReceipts([]);
      setTimeline([]);
      setQueueProgress(null);
      queueClosedRef.current = false;
      clearOutputBuffer();
      setRunId(resumeRunId);
      setRunStatus("queued");
      setLastEventId(resumeLastEventId ?? null);
      setTimelineMessageId(null);
      setMessages((x) => {
        const last = x[x.length - 1];
        if (last && last.role === "assistant") return x;
        const assistantMessageId = `resume-assistant-${Date.now()}`;
        return [
          ...x,
          {
            id: assistantMessageId,
            role: "assistant" as const,
            content: "",
          },
        ];
      });
      try {
        await streamRunEvents(
          resumeRunId,
          {
            onRunId: () => {
            },
            onEvent: (ev) => handleEvent(ev, resumeRunId),
          },
          resumeLastEventId,
        );
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
      } finally {
        inFlightRef.current = false;
      }
    },
    [clearOutputBuffer, handleEvent, setMessages],
  );

  const recover = useCallback(async (conversationId: string) => {
    if (inFlightRef.current) return;
    try {
      const runs = await listConversationRuns(conversationId);
      const target = runs.find((run) => RESUMABLE_STATUSES.has(run.status));
      if (!target) return;
      await resume(target.id, target.last_event_id ?? undefined);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, [resume]);

  const reconnect = useCallback(async () => {
    if (!runId) return;
    setError(null);
    try {
      await streamRunEvents(
        runId,
        {
          onRunId: () => {
          },
          onEvent: (ev) => handleEvent(ev, runId),
        },
        lastEventId || undefined,
      );
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, [finalize, handleEvent, lastEventId, runId]);

  const cancel = useCallback(async () => {
    if (!runId) return;
    try {
      await cancelRun(runId);
      setRunStatus("cancelled");
      setPending(null);
      void refreshThread();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, [refreshThread, runId]);

  const resolveInput = useCallback(
    async (answer: unknown) => {
      if (!runId || !pending) return;
      try {
        await answerRun(runId, pending.request.input_id, { answer });
        const answerContent =
          typeof answer === "string" ? answer : JSON.stringify(answer, null, 2);
        const assistantMessageId = `input-assistant-${Date.now()}`;
        setMessages((messages) => [
          ...messages,
          {
            id: `input-answer-${Date.now()}`,
            role: "user",
            content: answerContent,
          },
          {
            id: assistantMessageId,
            role: "assistant",
            content: "",
          },
        ]);
        setTimeline([]);
        setTimelineMessageId(assistantMessageId);
        setPending(null);
        setRunStatus("running");
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
      }
    },
    [pending, runId],
  );

  const steer = useCallback(
    async (input: SendInput): Promise<SteerReceipt | null> => {
      if (!runId) return null;
      try {
        const attachments = input.attachments.map((item) => ({ ...item }));
        const steerId = `steer-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
        const receipt = await createSteer(runId, steerId, input.prompt, {
          fileIds: input.fileIds,
          skillIds: input.skillIds,
        });
        setSteerReceipts((prev) => {
          if (prev.some((r) => r.steer_id === receipt.steer_id)) return prev;
          return [...prev, receipt];
        });
        const assistantMessageId = `steer-assistant-${Date.now()}`;
        setMessages((messages) => [
          ...messages,
          {
            id: `steer-user-${Date.now()}`,
            role: "user",
            content: input.prompt,
            attachments_json: attachments,
          },
          {
            id: assistantMessageId,
            role: "assistant",
            content: "",
          },
        ]);
        setTimeline([]);
        setTimelineMessageId(assistantMessageId);
        clearResources();
        return receipt;
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
        return null;
      }
    },
    [runId, clearResources, setMessages],
  );

  const getSteerStatus = useCallback(
    async (steerId: string): Promise<SteerReceipt | null> => {
      if (!runId) return null;
      try {
        return await getSteer(runId, steerId);
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
        return null;
      }
    },
    [runId],
  );

  const reset = useCallback(() => {
    clearOutputBuffer();
    setPending(null);
    setError(null);
    setRunId(null);
    setRunStatus(null);
    setLastEventId(null);
    setSteerReceipts([]);
    setTimeline([]);
    setTimelineMessageId(null);
    setQueueProgress(null);
    queueClosedRef.current = false;
  }, [clearOutputBuffer]);

  useEffect(() => clearOutputBuffer, [clearOutputBuffer]);

  return {
    runId,
    runStatus,
    pending: pending?.request ?? null,
    error,
    lastEventId,
    steerReceipts,
    timeline,
    timelineMessageId,
    queueProgress,
    send,
    reconnect,
    resume,
    recover,
    cancel,
    resolveInput,
    steer,
    getSteerStatus,
    reset,
  };
}
