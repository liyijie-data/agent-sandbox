import type { RunStatus } from "../api/types";
import styles from "./components.module.css";

export type BadgeTone = "ok" | "run" | "warn" | "error" | "neutral";

const STATUS_TONE: Record<RunStatus, BadgeTone> = {
  creating: "neutral",
  queued: "neutral",
  preparing: "run",
  running: "run",
  awaiting_input: "warn",
  cancel_requested: "warn",
  succeeded: "ok",
  failed: "error",
  cancelled: "neutral",
  expired: "neutral",
};

export const STATUS_LABEL: Record<RunStatus, string> = {
  creating: "创建中",
  queued: "排队中",
  preparing: "准备运行",
  running: "运行中",
  awaiting_input: "等待输入",
  cancel_requested: "取消中",
  succeeded: "已完成",
  failed: "失败",
  cancelled: "已取消",
  expired: "已过期",
};

interface StatusBadgeProps {
  status?: RunStatus | null;
  label?: string;
  tone?: BadgeTone;
}

export default function StatusBadge({ status, label, tone }: StatusBadgeProps) {
  const resolvedTone: BadgeTone = status
    ? STATUS_TONE[status]
    : tone || "neutral";
  const text =
    label || (status ? STATUS_LABEL[status] : "") || "";
  if (!text) return null;
  return (
    <span
      className={[styles.badge, styles[`badge${resolvedTone}`]].join(" ")}
    >
      <span className={styles.badgeDot} aria-hidden />
      {text}
    </span>
  );
}
