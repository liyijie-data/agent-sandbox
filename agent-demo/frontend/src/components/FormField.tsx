import { type ReactNode } from "react";
import styles from "./components.module.css";

interface FormFieldProps {
  label: string;
  htmlFor?: string;
  count?: { value: number; max: number };
  hint?: ReactNode;
  children: ReactNode;
  id?: string;
}

export default function FormField({
  label,
  htmlFor,
  count,
  hint,
  children,
  id,
}: FormFieldProps) {
  const mid = htmlFor || id;
  return (
    <div className={styles.formField}>
      <div className={styles.formLabelRow}>
        <label className={styles.formLabel} htmlFor={mid}>
          {label}
        </label>
        {count ? (
          <span className={styles.formCounter}>
            {count.value}/{count.max}
          </span>
        ) : null}
      </div>
      {children}
      {hint ? <div className={styles.formHint}>{hint}</div> : null}
    </div>
  );
}