import { X } from "@phosphor-icons/react";
import styles from "./composer.module.css";

export default function ReferenceChip({
  kind,
  label,
  onRemove,
}: {
  kind: "skill" | "tool";
  label: string;
  onRemove: () => void;
}) {
  return (
    <span className={styles.referenceChip}>
      <span className={styles.referenceTag}>
        {kind === "skill" ? "Skill" : "工具"}
      </span>
      <span className={styles.referenceLabel}>{label}</span>
      <button
        type="button"
        className={styles.referenceRemove}
        aria-label={`移除 ${label}`}
        onClick={onRemove}
      >
        <X size={11} weight="bold" />
      </button>
    </span>
  );
}