import {
  ChatsCircle,
  FileText,
  Gear,
  Plus,
} from "@phosphor-icons/react";
import type { Conversation } from "../../api/types";
import styles from "./chat.module.css";

interface ConversationSidebarProps {
  conversations: Conversation[];
  active: Conversation | null;
  settingsActive: boolean;
  onSelect: (conversation: Conversation) => void;
  onNewConversation: () => void;
  onOpenSettings: () => void;
}

export default function ConversationSidebar({
  conversations,
  active,
  settingsActive,
  onSelect,
  onNewConversation,
  onOpenSettings,
}: ConversationSidebarProps) {
  return (
    <aside className={styles.sidebar}>
      <div className={styles.brand}>
        <span className={styles.brandMark}>
          <ChatsCircle size={18} weight="fill" />
        </span>
        <span className={styles.brandName}>Agent Demo</span>
      </div>

      <button
        type="button"
        className={styles.newChatBtn}
        onClick={onNewConversation}
      >
        <Plus size={16} weight="bold" />
        新会话
      </button>

      <div className={styles.sideTitle}>会话历史</div>
      <nav className={styles.convList} aria-label="会话历史">
        {conversations.length === 0 ? (
          <div className={styles.sideEmpty}>暂无会话</div>
        ) : (
          conversations.map((conversation) => {
            const isActive = active?.id === conversation.id;
            return (
              <button
                key={conversation.id}
                type="button"
                className={
                  isActive ? styles.convItemActive : styles.convItem
                }
                onClick={() => onSelect(conversation)}
              >
                <FileText size={15} className={styles.convItemIcon} />
                <span className={styles.convItemTitle}>{conversation.title}</span>
              </button>
            );
          })
        )}
      </nav>

      <div className={styles.sidebarBottom}>
        <button
          type="button"
          className={
            settingsActive ? styles.settingsActive : styles.settingsEntry
          }
          onClick={onOpenSettings}
        >
          <Gear size={16} />
          <span>设置</span>
        </button>
      </div>
    </aside>
  );
}