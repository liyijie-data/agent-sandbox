import { FileArrowUp, Wrench } from "@phosphor-icons/react";
import type { AttachmentMeta, Message } from "../../api/types";
import { parseAttachments } from "../../lib/attachments";
import styles from "./chat.module.css";

const KIND_LABEL: Record<string, string> = {
  file: "文件",
  skill: "Skill",
  openapi: "OpenAPI",
  tool: "工具",
};

export default function UserMessage({ message }: { message: Message }) {
  const attachments = parseAttachments(message.attachments_json);
  return (
    <div className={styles.userRow}>
      <div className={styles.userBubble}>
        <div className={styles.userContent}>{message.content}</div>
        {attachments.length ? (
          <div className={styles.userChips}>
            {attachments.map((item: AttachmentMeta) => (
              <span key={item.id} className={styles.userChip}>
                {item.kind === "tool" ? <Wrench size={12} /> : <FileArrowUp size={12} />}
                {KIND_LABEL[item.kind] || item.kind}
                <span className={styles.userChipName} title={item.name}>{item.name}</span>
              </span>
            ))}
          </div>
        ) : null}
      </div>
    </div>
  );
}
