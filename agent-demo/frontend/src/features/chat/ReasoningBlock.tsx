import { CaretRight, CaretDown } from "@phosphor-icons/react";
import { useState } from "react";
import styles from "./chat.module.css";

export default function ReasoningBlock({ content }: { content: string }) {
  const [open, setOpen] = useState(true);
  if (!content) return null;
  return (
    <div className={styles.reasoning}>
      <button
        type="button"
        className={styles.reasoningHeader}
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        {open ? (
          <CaretDown size={13} weight="bold" />
        ) : (
          <CaretRight size={13} weight="bold" />
        )}
        思考过程
      </button>
      {open ? <div className={styles.reasoningBody}>{content}</div> : null}
    </div>
  );
}