import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from "react";
import styles from "./components.module.css";

type Variant = "primary" | "outline" | "ghost" | "danger";
type Size = "sm" | "md" | "lg";

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant;
  size?: Size;
  loading?: boolean;
  children?: ReactNode;
}

const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = "primary", size = "md", loading = false, className, children, disabled, ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      disabled={disabled || loading}
      className={[
        styles.btn,
        styles[variant],
        styles[size],
        className || "",
      ].join(" ")}
      {...rest}
    >
      {loading ? <span aria-hidden className={styles.spinner} /> : null}
      {children}
    </button>
  );
});

export default Button;