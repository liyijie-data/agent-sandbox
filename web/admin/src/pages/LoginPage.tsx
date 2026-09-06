import { FormEvent, useState } from "react";
import { Cube } from "@phosphor-icons/react";
import { login } from "../api/client";

export default function LoginPage({ onLoggedIn }: { onLoggedIn: () => void }) {
  const [token, setToken] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await login(token);
      onLoggedIn();
    } catch (err) {
      setError(err instanceof Error ? err.message : "登录失败");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="login-wrap">
      <form className="login-card" onSubmit={submit}>
        <div className="brand-mark lg"><Cube size={24} weight="duotone" /></div>
        <h1>Agent Sandbox 平台管理后台</h1>
        <div className="sub">仅限平台管理员。输入平台 SERVICE_TOKEN；凭据只保存在页面内存，不会写入浏览器存储。</div>
        <input
          className="field mono"
          type="password"
          placeholder="SERVICE_TOKEN"
          value={token}
          onChange={(e) => setToken(e.target.value)}
          autoFocus
        />
        {error ? <div className="error-box">{error}</div> : null}
        <div className="mt">
          <button className="btn primary" type="submit" disabled={busy || !token}>
            {busy ? "校验中…" : "登录"}
          </button>
        </div>
      </form>
    </div>
  );
}
