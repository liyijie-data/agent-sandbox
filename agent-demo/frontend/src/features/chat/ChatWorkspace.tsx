import { CaretDown, SignOut } from "@phosphor-icons/react";
import { useEffect, useRef } from "react";
import { artifactDownloadUrl } from "../../api/runs";
import type {
  Artifact,
  Conversation,
  Message,
  Resource,
  ResourceKind,
  Tool,
} from "../../api/types";
import { baseName, forceDownload } from "../../lib/format";
import { parseAttachments } from "../../lib/attachments";
import { useRunStream } from "../../hooks/useRunStream";
import type { UseRunStreamOptions } from "../../hooks/hooks";
import Composer from "../composer/Composer";
import HumanInputPanel from "../human-input/HumanInputPanel";
import ConversationThread from "./ConversationThread";
import styles from "./chat.module.css";

interface ConversationsApi {
  active: Conversation | null;
  messages: Message[];
  artifactsByRun: Record<string, Artifact[]>;
  setMessages: UseRunStreamOptions["setMessages"];
  refreshThread: () => Promise<void>;
  createLocalConversation: (title: string) => Promise<Conversation>;
}

interface ResourcesApi {
  files: Resource[];
  readySkills: Resource[];
  tools: Tool[];
  selectedFiles: string[];
  selectedSkills: string[];
  selectedTools: string[];
  upload: (kind: ResourceKind, file: File) => Promise<unknown>;
  removeFile: (id: string) => void;
  toggleSkill: (id: string) => void;
  toggleTool: (id: string) => void;
  clearSelection: () => void;
}

const truncate = (title: string, len = 48) =>
  title.length > len ? `${title.slice(0, len)}…` : title;

export default function ChatWorkspace({
  conversations,
  resources,
  onLogout,
}: {
  conversations: ConversationsApi;
  resources: ResourcesApi;
  onLogout: () => void;
}) {
  const runStream = useRunStream({
    getActive: () => conversations.active,
    createConversation: conversations.createLocalConversation,
    setMessages: conversations.setMessages,
    refreshThread: conversations.refreshThread,
    clearResources: resources.clearSelection,
  });

  const recoveredRef = useRef<string | null>(null);
  const activeId = conversations.active?.id ?? null;
  useEffect(() => {
    runStream.reset();
    if (activeId && (recoveredRef.current !== activeId || !runStream.runId)) {
      recoveredRef.current = activeId;
      void runStream.recover(activeId);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeId]);

  const downloadArtifact = async (item: Artifact) => {
    try {
      const { download_url } = await artifactDownloadUrl(item.id);
      forceDownload(download_url, baseName(item.name));
    } catch (err) {
      window.alert(err instanceof Error ? err.message : String(err));
    }
  };

  const copyMessage = (content: string) => {
    void navigator.clipboard?.writeText(content).catch(() => undefined);
  };

  const sortableStatus =
    runStream.runStatus === "creating" || runStream.runStatus === "preparing" || runStream.runStatus === "running";
  const blocked =
    runStream.runStatus === "creating" ||
    runStream.runStatus === "queued" ||
    runStream.runStatus === "preparing" ||
    runStream.runStatus === "awaiting_input" ||
    runStream.runStatus === "cancel_requested";

  const steerMode = !!runStream.runId && runStream.runStatus === "running";

  const waitReasonLabel: Record<string, string> = {
    pool_unready: "执行池预热中",
    in_client_queue: "等待其他任务",
    quota_exceeded: "并发额度已满",
  };
  const queueText =
    runStream.runStatus === "queued" &&
    runStream.queueProgress &&
    runStream.queueProgress.queue_length > 0
      ? `排队中 · 第 ${runStream.queueProgress.queue_position}/${runStream.queueProgress.queue_length} 位${
          runStream.queueProgress.wait_reason
            ? ` · ${waitReasonLabel[runStream.queueProgress.wait_reason] ?? runStream.queueProgress.wait_reason}`
            : ""
        }`
      : "";

  return (
    <div className={styles.workspace}>
      <header className={styles.chatHeader}>
        <div className={styles.headerTitle}>
          <span>{truncate(conversations.active?.title || "新会话")}</span>
          <CaretDown size={15} className={styles.headerCaret} />
        </div>
        <div className={styles.headerActions}>
          <button
            type="button"
            className={styles.iconBtn}
            aria-label="退出登录"
            title="退出登录"
            onClick={onLogout}
          >
            <SignOut size={17} />
          </button>
        </div>
      </header>

      <ConversationThread
        messages={conversations.messages}
        artifactsByRun={conversations.artifactsByRun}
        runStatus={runStream.runStatus}
        isStreaming={sortableStatus}
        timeline={runStream.timeline}
        timelineMessageId={runStream.timelineMessageId}
        download={downloadArtifact}
        copy={copyMessage}
      />

      {runStream.pending ? (
        <HumanInputPanel
          request={runStream.pending}
          busy={false}
          onResolve={(answer) => void runStream.resolveInput(answer)}
          onCancel={() => void runStream.cancel()}
        />
      ) : null}

      {queueText ? (
        <div className={styles.queueNotice} role="status">
          {queueText}
        </div>
      ) : null}

      <Composer
        files={resources.files}
        readySkills={resources.readySkills}
        tools={resources.tools}
        selectedFiles={resources.selectedFiles}
        selectedSkills={resources.selectedSkills}
        selectedTools={resources.selectedTools}
        disabled={blocked}
        steerMode={steerMode}
        onRemoveFile={resources.removeFile}
        onToggleSkill={resources.toggleSkill}
        onToggleTool={resources.toggleTool}
        onUploadFile={(file) => void resources.upload("files", file)}
        onSend={(input) => {
          const existingIds = new Set(
            conversations.messages.flatMap((message) =>
              parseAttachments(message.attachments_json).map((item) => item.id),
            ),
          );
          const optimistic = {
            ...input,
            attachments: input.attachments.filter((item) => !existingIds.has(item.id)),
          };
          steerMode ? void runStream.steer(optimistic) : void runStream.send(optimistic);
        }}
      />

      {runStream.error ? (
        <div className={styles.notice} role="alert">
          {runStream.error}
        </div>
      ) : null}
    </div>
  );
}
