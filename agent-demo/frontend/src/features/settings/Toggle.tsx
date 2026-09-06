import styles from "./settings.module.css";

interface ToggleProps {
  checked: boolean;
  onChange: (checked: boolean) => void;
  label?: string;
  disabled?: boolean;
}

export default function Toggle({
  checked,
  onChange,
  label,
  disabled,
}: ToggleProps) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      className={[styles.toggle, checked ? styles.toggleOn : ""].join(" ")}
      onClick={() => onChange(!checked)}
    >
      <span className={styles.toggleThumb} />
    </button>
  );
}