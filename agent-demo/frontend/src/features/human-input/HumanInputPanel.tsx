import { WarningCircle, CaretRight, Check } from "@phosphor-icons/react";
import { useState } from "react";
import type { HumanInputRequest } from "../../api/types";
import { Button } from "../../components";
import styles from "./humanInput.module.css";

interface HumanInputPanelProps {
  request: HumanInputRequest;
  busy: boolean;
  onResolve: (answer: unknown) => void;
  onCancel: () => void;
}

function optionValue(option: unknown): unknown {
  if (option === null || typeof option !== "object") return option;
  const obj = option as Record<string, unknown>;
  if ("value" in obj) return obj.value;
  return obj;
}

function optionLabel(option: unknown): string {
  if (option === null || typeof option !== "object") return String(option);
  const obj = option as { label?: unknown; description?: unknown };
  return typeof obj.label === "string" && obj.label
    ? obj.label
    : typeof obj.description === "string" && obj.description
      ? obj.description
      : JSON.stringify(option);
}

export default function HumanInputPanel({
  request,
  busy,
  onResolve,
  onCancel,
}: HumanInputPanelProps) {
  const [custom, setCustom] = useState("");
  const options = Array.isArray(request.options) ? request.options : [];

  const submitCustom = () => {
    const value = custom.trim();
    if (!value) return;
    onResolve(value);
    setCustom("");
  };

  return (
    <div className={styles.panel} role="dialog" aria-label="等待你补充信息">
      <div className={styles.header}>
        <WarningCircle size={16} className={styles.icon} />
        <span className={styles.title}>需要你补充信息</span>
      </div>
      <p className={styles.prompt}>{request.prompt}</p>

      {options.length ? (
        <div className={styles.options}>
          {options.map((option, index) => {
            const recommended =
              option !== null &&
              typeof option === "object" &&
              (option as { recommended?: boolean }).recommended;
            const label = optionLabel(option);
            const description =
              option !== null && typeof option === "object"
                ? (option as { description?: string }).description
                : undefined;
            return (
              <button
                key={index}
                type="button"
                className={styles.option}
                disabled={busy}
                onClick={() => onResolve(optionValue(option))}
              >
                <span className={styles.optionNumber}>{index + 1}</span>
                <span className={styles.optionCopy}>
                  <span className={styles.optionLabel}>{label}</span>
                  {description ? (
                    <span className={styles.optionDescription}>
                      {description}
                    </span>
                  ) : null}
                </span>
                {recommended ? (
                  <span className={styles.recommended}>推荐</span>
                ) : null}
                <CaretRight size={14} className={styles.optionArrow} />
              </button>
            );
          })}
        </div>
      ) : null}

      <textarea
        className={styles.textarea}
        placeholder="或输入补充信息…"
        value={custom}
        disabled={busy}
        onChange={(e) => setCustom(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && !e.shiftKey) {
            e.preventDefault();
            submitCustom();
          }
        }}
      />

      <div className={styles.footer}>
        <Button variant="ghost" size="sm" disabled={busy} onClick={onCancel}>
          取消
        </Button>
        <Button
          size="sm"
          disabled={busy || !custom.trim()}
          onClick={submitCustom}
        >
          <Check size={15} />
          确认
        </Button>
      </div>
    </div>
  );
}
