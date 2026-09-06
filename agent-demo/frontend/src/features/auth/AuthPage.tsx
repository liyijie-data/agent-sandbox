import { ChatsCircle } from "@phosphor-icons/react";
import AuthForm from "./AuthForm";
import styles from "./auth.module.css";

export default function AuthPage({
  onAuthenticated,
}: {
  onAuthenticated: () => void;
}) {
  return (
    <div className={styles.wrapper}>
      <div className={styles.card}>
        <div className={styles.brand}>
          <span className={styles.brandMark}>
            <ChatsCircle size={20} weight="fill" />
          </span>
          <span className={styles.brandName}>Agent Demo</span>
        </div>
        <AuthForm onSuccess={onAuthenticated} />
      </div>
    </div>
  );
}