import { FileText, X } from "@phosphor-icons/react";
import type { Resource } from "../../api/types";
import { formatBytes } from "../../lib/format";
import styles from "./composer.module.css";

export default function AttachmentCard({
  file,
  onRemove,
}: {
  file: Resource;
  onRemove: () => void;
}) {
  return (
    <div className={styles.attachmentCard}>
      <FileText size={15} />
      <span className={styles.attachmentName} title={file.name}>{file.name}</span>
      {file.size_bytes > 0 ? (
        <span className={styles.attachmentSize}>
          {formatBytes(file.size_bytes)}
        </span>
      ) : null}
      <button
        type="button"
        className={styles.attachmentRemove}
        aria-label={`移除 ${file.name}`}
        onClick={onRemove}
      >
        <X size={13} weight="bold" />
      </button>
    </div>
  );
}
