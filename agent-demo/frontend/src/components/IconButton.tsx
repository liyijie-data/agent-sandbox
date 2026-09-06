import {
  forwardRef,
  type ButtonHTMLAttributes,
  type ReactNode,
} from "react";
import styles from "./components.module.css";

interface IconButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  label: string;
  children: ReactNode;
  tonal?: "default" | "solid";
}

const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(
  function IconButton(
    { label, children, tonal = "default", className, ...rest },
    ref,
  ) {
    return (
      <button
        ref={ref}
        type="button"
        aria-label={label}
        title={label}
        className={[
          styles.iconBtn,
          tonal === "solid" ? styles.iconBtnSolid : "",
          className || "",
        ].join(" ")}
        {...rest}
      >
        {children}
      </button>
    );
  },
);

export default IconButton;