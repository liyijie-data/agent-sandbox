import type { ReactNode } from "react";
import styles from "./components.module.css";

interface EmptyStateProps {
  icon?: ReactNode;
  title: string;
  description?: string;
  action?: ReactNode;
  compact?: boolean;
}

export default function EmptyState({
  icon,
  title,
  description,
  action,
  compact = false,
}: EmptyStateProps) {
  return (
    <div
      className={[
        styles.emptyState,
        compact ? styles.emptyStateCompact : "",
      ].join(" ")}
    >
      {icon ? <div className={styles.emptyIcon}>{icon}</div> : null}
      <div className={styles.emptyTitle}>{title}</div>
      {description ? (
        <div className={styles.emptyDescription}>{description}</div>
      ) : null}
      {action ? <div className={styles.emptyAction}>{action}</div> : null}
    </div>
  );
}