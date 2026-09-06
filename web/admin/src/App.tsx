import { useEffect, useState } from "react";
import { Cube, SignOut } from "@phosphor-icons/react";
import { logout as apiLogout, me } from "./api/client";
import LoginPage from "./pages/LoginPage";
import ClientsPage from "./pages/ClientsPage";
import ImagesPage from "./pages/ImagesPage";
import StatusPage from "./pages/StatusPage";

type Tab = "clients" | "images" | "status";

const TABS: Array<{ id: Tab; label: string }> = [
  { id: "clients", label: "接入方管理" },
  { id: "images", label: "镜像管理" },
  { id: "status", label: "平台状态" },
];

export default function App() {
  const [authed, setAuthed] = useState<boolean | null>(null);
  const [tab, setTab] = useState<Tab>("clients");

  useEffect(() => {
    void me()
      .then(setAuthed)
      .catch(() => setAuthed(false));
  }, []);

  if (authed === null) {
    return <div className="login-wrap"><span className="spinner" aria-label="加载中" /></div>;
  }
  if (!authed) {
    return <LoginPage onLoggedIn={() => setAuthed(true)} />;
  }

  return (
    <div className="app-shell">
      <header className="app-header">
        <div className="app-brand">
          <div className="brand-mark"><Cube size={18} weight="duotone" /></div>
          <div>
            <h1>Agent Sandbox 平台管理后台</h1>
            <div className="sub">管理员会话（Cookie）· 平台 /admin/v1 同源复用</div>
          </div>
        </div>
        <button
          className="btn"
          onClick={() => {
            void apiLogout().finally(() => setAuthed(false));
          }}
        >
          <SignOut size={14} /> 退出
        </button>
      </header>
      <nav className="tabs">
        {TABS.map((t) => (
          <button key={t.id} className={tab === t.id ? "active" : ""} onClick={() => setTab(t.id)}>
            {t.label}
          </button>
        ))}
      </nav>
      {tab === "clients" ? <ClientsPage /> : tab === "images" ? <ImagesPage /> : <StatusPage />}
    </div>
  );
}
