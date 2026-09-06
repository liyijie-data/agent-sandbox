import { Paperclip } from "@phosphor-icons/react";
import type { Resource, Tool } from "../../api/types";
import styles from "./composer.module.css";

interface ResourceMenuProps {
  open: boolean;
  readySkills: Resource[];
  tools: Tool[];
  selectedSkills: string[];
  selectedTools: string[];
  onToggleSkill: (id: string) => void;
  onToggleTool: (id: string) => void;
  onUpload: () => void;
  onClose: () => void;
}

export default function ResourceMenu({
  open,
  readySkills,
  tools,
  selectedSkills,
  selectedTools,
  onToggleSkill,
  onToggleTool,
  onUpload,
  onClose,
}: ResourceMenuProps) {
  if (!open) return null;
  return (
    <div className={styles.resourceMenu} role="menu" aria-label="资源菜单">
      <button
        type="button"
        className={styles.menuUpload}
        onClick={() => {
          onClose();
          onUpload();
        }}
      >
        <Paperclip size={15} />
        上传附件
      </button>

      <div className={styles.menuGroup}>
        <div className={styles.menuGroupTitle}>选择 Skill</div>
        {readySkills.length === 0 ? (
          <div className={styles.menuEmpty}>暂无可用的 Skill</div>
        ) : (
          readySkills.map((skill) => (
            <button
              key={skill.id}
              type="button"
              className={styles.menuOption}
              onClick={() => onToggleSkill(skill.id)}
            >
              <span
                className={[
                  styles.checkbox,
                  selectedSkills.includes(skill.id) ? styles.checkboxOn : "",
                ].join(" ")}
              />
              <span className={styles.menuKind}>Skill</span>
              <span className={styles.menuName}>{skill.name}</span>
            </button>
          ))
        )}
      </div>

      <div className={styles.menuGroup}>
        <div className={styles.menuGroupTitle}>选择 MCP / OpenAPI 工具</div>
        {tools.length === 0 ? (
          <div className={styles.menuEmpty}>暂无可用的工具</div>
        ) : (
          tools.map((tool) => (
            <button
              key={tool.id}
              type="button"
              className={styles.menuOption}
              onClick={() => onToggleTool(tool.id)}
            >
              <span
                className={[
                  styles.checkbox,
                  selectedTools.includes(tool.id) ? styles.checkboxOn : "",
                ].join(" ")}
              />
              <span className={styles.menuKind}>
                {tool.type === "mcp" ? "MCP" : "OpenAPI"}
              </span>
              <span className={styles.menuName}>{tool.name}</span>
            </button>
          ))
        )}
      </div>
    </div>
  );
}
