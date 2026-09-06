import { useEffect, useState } from "react";
import { clearToken, getToken } from "../api/client";
import { logout } from "../api/auth";
import { useConversations } from "../hooks/useConversations";
import { useResources } from "../hooks/useResources";
import ConversationSidebar from "../features/chat/ConversationSidebar";
import ChatWorkspace from "../features/chat/ChatWorkspace";
import SettingsPage from "../features/settings/SettingsPage";
import type { AppView, SettingsTab } from "./routes";
import styles from "./AppShell.module.css";

export default function AppShell({ onLogout }: { onLogout: () => void }) {
  const [view, setView] = useState<AppView>("chat");
  const [settingsTab, setSettingsTab] = useState<SettingsTab>("skills");
  const conversations = useConversations();
  const resources = useResources();

  useEffect(() => {
    void conversations.loadAll();
    void resources.loadAll();
  }, []);

  const handleNewConversation = () => {
    conversations.newConversation();
    setView("chat");
  };

  const handleLogout = () => {
    clearToken();
    void logout().catch(() => undefined);
    if (getToken()) clearToken();
    onLogout();
  };

  return (
    <div className={styles.shell}>
      <ConversationSidebar
        conversations={conversations.conversations}
        active={conversations.active}
        onSelect={(conversation) => void conversations.select(conversation)}
        onNewConversation={handleNewConversation}
        onOpenSettings={() => setView("settings")}
        settingsActive={view === "settings"}
      />
      <main className={styles.main}>
        {view === "settings" ? (
          <SettingsPage
            resources={resources}
            tab={settingsTab}
            onTabChange={setSettingsTab}
            onBack={() => setView("chat")}
          />
        ) : (
          <ChatWorkspace
            conversations={conversations}
            resources={resources}
            onLogout={handleLogout}
          />
        )}
      </main>
    </div>
  );
}