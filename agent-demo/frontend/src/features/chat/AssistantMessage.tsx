import { CaretDown, CaretRight, Copy, Wrench } from "@phosphor-icons/react";
import { useState } from "react";
import type { Artifact, Message, StreamTimelineItem } from "../../api/types";
import Markdown from "../../lib/Markdown";
import ArtifactLink from "./ArtifactLink";
import ReasoningBlock from "./ReasoningBlock";
import styles from "./chat.module.css";

interface AssistantMessageProps {
  message: Message;
  artifacts: Artifact[];
  isStreaming: boolean;
  timeline: StreamTimelineItem[];
  onDownload: (item: Artifact) => void;
  onCopy: (content: string) => void;
}

export default function AssistantMessage({
  message,
  artifacts,
  isStreaming,
  timeline,
  onDownload,
  onCopy,
}: AssistantMessageProps) {
  return (
    <div className={styles.assistantRow}>
      <div className={styles.avatar} aria-hidden>
        A
      </div>
      <div className={styles.assistantBody}>
        {timeline.length ? (
          <StreamTimeline items={timeline} />
        ) : (
          <>
            <ReasoningBlock content={message.reasoning_content || ""} />
            <Markdown content={message.content} />
          </>
        )}
        {isStreaming && !message.content.trim() && !timeline.length ? (
          <div className={styles.streamingHint}>正在生成…</div>
        ) : null}
        {artifacts.length ? (
          <div className={styles.artifactsRow}>
            {artifacts.map((artifact) => (
              <ArtifactLink
                key={artifact.id}
                artifact={artifact}
                onDownload={onDownload}
              />
            ))}
          </div>
        ) : null}
        {message.content.trim() ? (
          <div className={styles.assistantActions}>
            <button
              type="button"
              className={styles.assistantAction}
              aria-label="复制"
              title="复制"
              onClick={() => onCopy(message.content)}
            >
              <Copy size={14} />
            </button>
          </div>
        ) : null}
      </div>
    </div>
  );
}

function StreamTimeline({ items }: { items: StreamTimelineItem[] }) {
  return (
    <>
      {items.map((item) => {
        if (item.kind === "reasoning") {
          return <ReasoningBlock key={item.id} content={item.content} />;
        }
        if (item.kind === "content") {
          return <Markdown key={item.id} content={item.content} />;
        }
        if (item.kind === "input_request") {
          return <InputRequestBlock key={item.id} item={item} />;
        }
        if (item.kind === "tool") {
          return <ToolBlock key={item.id} item={item} />;
        }
        return null;
      })}
    </>
  );
}

function InputRequestBlock({
  item,
}: {
  item: Extract<StreamTimelineItem, { kind: "input_request" }>;
}) {
  const [open, setOpen] = useState(true);
  return (
    <div className={styles.inputRequestBlock}>
      <button type="button" className={styles.inputRequestHeader} aria-expanded={open} onClick={() => setOpen((value) => !value)}>
        {open ? <CaretDown size={13} weight="bold" /> : <CaretRight size={13} weight="bold" />}
        Agent 提问
      </button>
      {open ? <div className={styles.inputRequestBody}>{item.prompt}</div> : null}
    </div>
  );
}

export function ToolBlock({
  item,
}: {
  item: Extract<StreamTimelineItem, { kind: "tool" }>;
}) {
  const [open, setOpen] = useState(false);
  const status = item.status === "started" ? "调用中" : item.status === "succeeded" ? "成功" : "失败";
  return (
    <div className={styles.toolBlock}>
      <button
        type="button"
        className={styles.toolHeader}
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
      >
        {open ? <CaretDown size={13} weight="bold" /> : <CaretRight size={13} weight="bold" />}
        <Wrench size={14} />
        <span>工具 · {item.display_tool_name || item.operation || item.tool_name}{item.operation && item.display_tool_name ? ` · ${item.operation}` : ""}</span>
        <span className={item.status === "failed" ? styles.toolFailed : styles.toolStatus}>{status}</span>
      </button>
      {open ? (
        <div className={styles.toolBody}>
          {item.input !== undefined ? <ToolField label="传入参数" value={item.input} /> : null}
          {item.result !== undefined ? <ToolField label="返回结果" value={item.result} /> : null}
          {item.duration_ms !== undefined ? <div className={styles.toolFieldLabel}>耗时：{item.duration_ms} ms</div> : null}
          {item.details_truncated ? <div className={styles.toolUnavailable}>详情已截断，完整内容请查看 trace。</div> : null}
          {item.error_type ? <div className={styles.toolError}>错误：{item.error_type}</div> : null}
          {item.input === undefined && item.result === undefined && !item.error_type ? (
            <div className={styles.toolUnavailable}>该事件未提供参数或返回结果。</div>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

function ToolField({ label, value }: { label: string; value: unknown }) {
  const text = typeof value === "string" ? value : JSON.stringify(value, null, 2);
  return (
    <div className={styles.toolField}>
      <div className={styles.toolFieldLabel}>{label}</div>
      <pre>{text}</pre>
    </div>
  );
}
