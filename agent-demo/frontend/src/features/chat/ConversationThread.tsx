import { useEffect, useRef } from "react";
import { ChatCircleText } from "@phosphor-icons/react";
import type { Artifact, Message, RunStatus, StreamTimelineItem } from "../../api/types";
import AssistantMessage, { ToolBlock } from "./AssistantMessage";
import UserMessage from "./UserMessage";
import styles from "./chat.module.css";

interface ConversationThreadProps {
  messages: Message[];
  artifactsByRun: Record<string, Artifact[]>;
  runStatus: RunStatus | null;
  isStreaming: boolean;
  timeline: StreamTimelineItem[];
  timelineMessageId: string | null;
  download: (item: Artifact) => void;
  copy: (content: string) => void;
}

export default function ConversationThread({
  messages,
  artifactsByRun,
  isStreaming,
  timeline,
  timelineMessageId,
  download,
  copy,
}: ConversationThreadProps) {
  const threadRef = useRef<HTMLElement>(null);
  const pinnedRef = useRef(true);
  const last = messages[messages.length - 1];
  const activeTimelineMessageId = timelineMessageId ?? [...messages].reverse().find(
    (message) => message.role === "assistant",
  )?.id;
  const activeTimelineRunId = messages.find((message) => message.id === activeTimelineMessageId)?.run_id ?? null;

  const scrollToBottom = () => {
    const host = threadRef.current;
    if (host) host.scrollTop = host.scrollHeight;
  };

  const handleScroll = () => {
    const host = threadRef.current;
    if (!host) return;
    const distance = host.scrollHeight - host.scrollTop - host.clientHeight;
    pinnedRef.current = distance < 48;
  };

  useEffect(() => {
    if (pinnedRef.current) scrollToBottom();
  }, [messages.length, last?.content, last?.reasoning_content, timeline]);

  return (
    <section
      className={styles.thread}
      ref={threadRef}
      onScroll={handleScroll}
      aria-live="polite"
    >
      <div className={styles.threadColumn}>
        {messages.length === 0 ? (
          <div className={styles.threadEmpty}>
            <ChatCircleText size={30} />
            <p>输入任务开始对话，或从左侧选择历史会话。</p>
          </div>
        ) : (
          messages.map((message, index) => {
            if (message.role === "user") {
              return <UserMessage key={message.id + index} message={message} />;
            }
            if (message.role === "assistant") {
              return (
                <AssistantMessage
                  key={message.id + index}
                  message={message}
                  artifacts={
                    message.run_id ? artifactsByRun[message.run_id] || [] : []
                  }
                  isStreaming={isStreaming && message.id === activeTimelineMessageId}
                  timeline={message.id === activeTimelineMessageId ? timeline : []}
                  onDownload={download}
                  onCopy={copy}
                />
              );
            }
            if (message.role === "system") {
              return <SystemTimelineMessage key={message.id + index} message={message} hiddenCallIds={new Set(timeline.filter((item) => item.kind === "tool").map((item) => item.tool_call_id))} hiddenRunId={activeTimelineRunId} />;
            }
            return null;
          })
        )}
      </div>
    </section>
  );
}

function InputRequestMessage({ message }: { message: Message }) {
  let metadata: { type?: string; kind?: string } | null = null;
  try {
    metadata = typeof message.attachments_json === "string"
      ? JSON.parse(message.attachments_json) as { type?: string; kind?: string }
      : message.attachments_json as { type?: string; kind?: string } | null;
  } catch {
    return null;
  }
  if (metadata?.type !== "input_request") return null;
  return (
    <div className={styles.inputRequestHistory}>
      <div className={styles.inputRequestHistoryTitle}>Agent 提问</div>
      <div>{message.content}</div>
    </div>
  );
}

function SystemTimelineMessage({ message, hiddenCallIds, hiddenRunId }: { message: Message; hiddenCallIds: Set<string>; hiddenRunId: string | null | undefined }) {
  let metadata: Record<string, unknown> | null = null;
  try {
    metadata = (typeof message.attachments_json === "string"
      ? JSON.parse(message.attachments_json)
      : message.attachments_json) as Record<string, unknown> | null;
  } catch { return null; }
  if (metadata?.type === "input_request") return <InputRequestMessage message={message} />;
  if (metadata?.type === "run_error") return <div className={styles.inputRequestHistory}>{message.content}</div>;
  if (metadata?.type === "resource_notice") return <div className={styles.inputRequestHistory}>{message.content}</div>;
  if (metadata?.type !== "tool_timeline") return null;
  if (metadata.run_id === hiddenRunId && typeof metadata.tool_call_id === "string" && hiddenCallIds.has(metadata.tool_call_id)) return null;
  if (metadata.status !== "started" && metadata.status !== "succeeded" && metadata.status !== "failed") return null;
  return <ToolBlock item={{
    kind: "tool", id: `history-tool-${message.id}`, tool_call_id: String(metadata.tool_call_id || ""),
    tool_name: String(metadata.tool_name || message.content), status: metadata.status,
    ...(typeof metadata.display_tool_name === "string" ? { display_tool_name: metadata.display_tool_name } : {}),
    ...(typeof metadata.model_tool_name === "string" ? { model_tool_name: metadata.model_tool_name } : {}),
    ...(typeof metadata.operation === "string" ? { operation: metadata.operation } : {}),
    ...(metadata.arguments !== undefined ? { input: metadata.arguments } : {}),
    ...(metadata.result !== undefined ? { result: metadata.result } : {}),
    ...(typeof metadata.duration_ms === "number" ? { duration_ms: metadata.duration_ms } : {}),
    ...(metadata.details_truncated === true ? { details_truncated: true } : {}),
    ...(typeof metadata.error_type === "string" ? { error_type: metadata.error_type } : {}),
  }} />;
}
