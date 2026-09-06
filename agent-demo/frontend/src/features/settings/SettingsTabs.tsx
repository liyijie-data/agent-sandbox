import { SETTINGS_TABS, type SettingsTab } from "../../app/routes";
import styles from "./settings.module.css";

interface SettingsTabsProps {
  tab: SettingsTab;
  onChange: (tab: SettingsTab) => void;
}

export default function SettingsTabs({ tab, onChange }: SettingsTabsProps) {
  return (
    <nav className={styles.tabs} role="tablist" aria-label="设置分类">
      {SETTINGS_TABS.map((item) => (
        <button
          key={item.id}
          type="button"
          role="tab"
          aria-selected={tab === item.id}
          className={tab === item.id ? styles.tabActive : styles.tab}
          onClick={() => onChange(item.id)}
        >
          {item.label}
        </button>
      ))}
    </nav>
  );
}