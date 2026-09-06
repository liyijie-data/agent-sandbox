import { useState } from "react";
import { getToken } from "../api/client";
import AuthPage from "../features/auth/AuthPage";
import AppShell from "./AppShell";

export default function App() {
  const [token, setToken] = useState<string | null>(getToken());

  if (!token) {
    return <AuthPage onAuthenticated={() => setToken(getToken())} />;
  }
  return <AppShell onLogout={() => setToken(null)} />;
}