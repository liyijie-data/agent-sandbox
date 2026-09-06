import { ShieldCheck } from "@phosphor-icons/react";
import { useState } from "react";
import { login, register } from "../../api/auth";
import Button from "../../components/Button";
import style from "./auth.module.css";

interface AuthFormProps {
  onSuccess: () => void;
}

export default function AuthForm({ onSuccess }: AuthFormProps) {
  const [mode, setMode] = useState<"login" | "register">("login");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  const disabled = loading || !username || !password;

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError("");
    setLoading(true);
    try {
      if (mode === "login") await login(username, password);
      else await register(username, password);
      onSuccess();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  };

  const toggleMode = () => {
    setMode((m) => (m === "login" ? "register" : "login"));
    setError("");
  };

  return (
    <form className={style.form} onSubmit={submit}>
      <div className={style.title}>{mode === "login" ? "登录" : "注册"}</div>

      <div className={style.field}>
        <label className={style.label} htmlFor="auth-username">
          用户名
        </label>
        <input
          id="auth-username"
          className={style.input}
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          autoComplete="username"
          autoFocus
          required
        />
      </div>

      <div className={style.field}>
        <label className={style.label} htmlFor="auth-password">
          密码
        </label>
        <input
          id="auth-password"
          type="password"
          className={style.input}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          autoComplete={mode === "login" ? "current-password" : "new-password"}
          required
        />
      </div>

      {error ? (
        <div className={style.error} role="alert">
          {error}
        </div>
      ) : null}

      <Button type="submit" disabled={disabled} loading={loading} className={style.submit}>
        <ShieldCheck size={16} />
        {loading ? "处理中…" : mode === "login" ? "登录" : "注册并登录"}
      </Button>

      <button type="button" className={style.toggle} onClick={toggleMode}>
        {mode === "login" ? "没有账号？去注册" : "已有账号？去登录"}
      </button>
    </form>
  );
}