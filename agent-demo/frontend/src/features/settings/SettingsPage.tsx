import { ArrowLeft } from "@phosphor-icons/react";
import type { SettingsTab } from "../../app/routes";
import { Button } from "../../components";
import McpPanel from "./McpPanel";
import NetworkPanel from "./NetworkPanel";
import OpenApiPanel from "./OpenApiPanel";
import SettingsTabs from "./SettingsTabs";
import SkillsPanel from "./SkillsPanel";
import styles from "./settings.module.css";
import type { SettingsResourcesApi } from "./panelProps";

interface SettingsPageProps {
  resources: SettingsResourcesApi;
  tab: SettingsTab;
  onTabChange: (tab: SettingsTab) => void;
  onBack: () => void;
}

export default function SettingsPage({
  resources,
  tab,
  onTabChange,
  onBack,
}: SettingsPageProps) {
  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <h1>设置</h1>
          <p>管理 Agent 的技能、工具与接口配置，让 Agent 更懂你的业务。</p>
        </div>
        <Button variant="outline" onClick={onBack}>
          <ArrowLeft size={15} />
          返回聊天
        </Button>
      </header>

      <SettingsTabs tab={tab} onChange={onTabChange} />

      <div className={styles.content}>
        {tab === "skills" ? (
          <SkillsPanel
            readySkills={resources.readySkills}
            upload={resources.upload}
          />
        ) : tab === "mcp" ? (
          <McpPanel tools={resources.tools} refresh={resources.loadAll} />
        ) : tab === "network" ? (
          <NetworkPanel />
        ) : (
          <OpenApiPanel
            tools={resources.tools}
            readySpecs={resources.readySpecs}
            upload={resources.upload}
            refresh={resources.loadAll}
          />
        )}
      </div>
    </div>
  );
}