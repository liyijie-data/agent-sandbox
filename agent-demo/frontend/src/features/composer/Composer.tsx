import {
  PaperPlaneRight,
  Plus,
} from "@phosphor-icons/react";
import { useEffect, useRef, useState } from "react";
import type { Resource, Tool } from "../../api/types";
import type { AttachmentKind } from "../../api/types";
import type { SendInput } from "../../hooks/hooks";
import AttachmentCard from "./AttachmentCard";
import ReferenceChip from "./ReferenceChip";
import ResourceMenu from "./ResourceMenu";
import SlashPicker, { type PickerItem } from "./SlashPicker";
import ModelSettings, { type ModelSettingsValue } from "./ModelSettings";
import styles from "./composer.module.css";

type PickerScope = "all" | "skill" | "tool";

interface ComposerProps {
  files: Resource[];
  readySkills: Resource[];
  tools: Tool[];
  selectedFiles: string[];
  selectedSkills: string[];
  selectedTools: string[];
  disabled: boolean;
  steerMode?: boolean;
  onRemoveFile: (id: string) => void;
  onToggleSkill: (id: string) => void;
  onToggleTool: (id: string) => void;
  onUploadFile: (file: File) => void;
  onSend: (input: SendInput) => void;
}

export default function Composer({
  files,
  readySkills,
  tools,
  selectedFiles,
  selectedSkills,
  selectedTools,
  disabled,
  steerMode = false,
  onRemoveFile,
  onToggleSkill,
  onToggleTool,
  onUploadFile,
  onSend,
}: ComposerProps) {
  const [prompt, setPrompt] = useState("");
  const [menuOpen, setMenuOpen] = useState(false);
  const [pickerOpen, setPickerOpen] = useState(false);
  const [pickerScope, setPickerScope] = useState<PickerScope>("all");
  const [pickerQuery, setPickerQuery] = useState("");
  const [modelSettings, setModelSettings] = useState<ModelSettingsValue>({});
  const [modelSettingsValid, setModelSettingsValid] = useState(true);
  const addButtonRef = useRef<HTMLButtonElement>(null);
  const resourceMenuRef = useRef<HTMLDivElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  useEffect(() => {
    if (!menuOpen) return;
    const handlePointerDown = (event: MouseEvent) => {
      const target = event.target as Node;
      if (
        !addButtonRef.current?.contains(target) &&
        !resourceMenuRef.current?.contains(target)
      ) {
        setMenuOpen(false);
      }
    };
    document.addEventListener("mousedown", handlePointerDown);
    return () => document.removeEventListener("mousedown", handlePointerDown);
  }, [menuOpen]);

  const selectedFileObjs = files.filter((f) => selectedFiles.includes(f.id));
  const selectedSkillObjs = readySkills.filter((s) =>
    selectedSkills.includes(s.id),
  );
  const selectedToolObjs = tools.filter((t) => selectedTools.includes(t.id));

  const menuTools = steerMode ? [] : tools;

  const pickerItems: PickerItem[] = [
    ...readySkills.map((skill) => ({
      id: skill.id,
      name: skill.name,
      kind: "skill" as const,
      selected: selectedSkills.includes(skill.id),
    })),
    ...menuTools.map((tool) => ({
      id: tool.id,
      name: tool.name,
      kind: "tool" as const,
      selected: selectedTools.includes(tool.id),
    })),
  ];
  const visiblePickerItems = pickerItems.filter((item) =>
    pickerScope === "all" ? true : item.kind === pickerScope,
  );

  const triggerUpload = () => fileInputRef.current?.click();
  const closePickers = () => {
    setPickerOpen(false);
    setMenuOpen(false);
  };

  const togglePickerItem = (item: PickerItem) => {
    if (item.kind === "skill") onToggleSkill(item.id);
    else if (!steerMode) onToggleTool(item.id);
  };

  const submit = () => {
    const text = prompt.trim();
    const hasResources =
      selectedFileObjs.length > 0 ||
      selectedSkillObjs.length > 0 ||
      selectedToolObjs.length > 0;
    if ((!text && !hasResources) || disabled || !modelSettingsValid) return;
    const attachments: Array<{ id: string; name: string; kind: AttachmentKind }> = [
        ...selectedFileObjs.map((f) => ({
          id: f.id,
          name: f.name,
          kind: "file" as const,
        })),
        ...selectedSkillObjs.map((s) => ({
          id: s.id,
          name: s.name,
          kind: "skill" as const,
        })),
        ...selectedToolObjs.map((tool) => ({
          id: tool.id,
          name: tool.name,
          kind: "tool" as const,
        })),
    ];
    onSend({
      prompt: text,
      attachments: attachments.map((item) => ({ ...item })),
      fileIds: selectedFileObjs.map((file) => file.id),
      skillIds: selectedSkillObjs.map((skill) => skill.id),
      toolIds: steerMode ? [] : selectedToolObjs.map((tool) => tool.id),
      ...modelSettings,
    });
    setPrompt("");
    setPickerOpen(false);
    setMenuOpen(false);
    if (textareaRef.current) textareaRef.current.style.height = "auto";
  };

  return (
    <div className={styles.composer}>
      <input
        ref={fileInputRef}
        type="file"
        className={styles.hiddenInput}
        onChange={(e) => {
          const file = e.target.files?.[0];
          if (file) void onUploadFile(file);
          e.currentTarget.value = "";
        }}
      />

      <SlashPicker
        open={pickerOpen}
        scope={pickerScope}
        query={pickerQuery}
        items={pickerItems}
        onQueryChange={setPickerQuery}
        onToggle={togglePickerItem}
        onEnter={() => {
          const first = visiblePickerItems[0];
          if (first) togglePickerItem(first);
        }}
        onClose={closePickers}
      />

      <div ref={resourceMenuRef}>
        <ResourceMenu
          open={menuOpen}
          readySkills={readySkills}
          tools={menuTools}
          selectedSkills={selectedSkills}
          selectedTools={selectedTools}
          onToggleSkill={onToggleSkill}
          onToggleTool={onToggleTool}
          onUpload={triggerUpload}
          onClose={closePickers}
        />
      </div>

      {selectedFileObjs.length || selectedSkillObjs.length || selectedToolObjs.length ? (
        <div className={styles.selectedResourcesRow} aria-label="已选择资源">
          {selectedFileObjs.map((file) => (
            <AttachmentCard
              key={file.id}
              file={file}
              onRemove={() => onRemoveFile(file.id)}
            />
          ))}
          {selectedSkillObjs.map((skill) => (
            <ReferenceChip
              key={skill.id}
              kind="skill"
              label={skill.name}
              onRemove={() => onToggleSkill(skill.id)}
            />
          ))}
          {selectedToolObjs.map((tool) => (
            <ReferenceChip
              key={tool.id}
              kind="tool"
              label={tool.name}
              onRemove={() => onToggleTool(tool.id)}
            />
          ))}
        </div>
      ) : null}

      {steerMode && selectedToolObjs.length ? (
        <div className={styles.steerToolHint}>
          运行中不支持新增工具，已忽略工具选择
        </div>
      ) : null}

      <div className={styles.inputRow}>
        <textarea
          ref={textareaRef}
          className={styles.textarea}
          value={prompt}
          placeholder={
            steerMode
              ? "输入引导消息，将在下一个安全节点纳入上下文…"
              : "输入任务；可附加文件、Skill 或工具"
          }
          disabled={disabled}
          rows={1}
          onChange={(e) => {
            const value = e.target.value;
            setPrompt(value);
            const el = e.currentTarget;
            el.style.height = "auto";
            el.style.height = `${el.scrollHeight}px`;
            const match = value.match(/(^|\s)(\/[\w]*)$/);
            if (match) {
              setPickerScope("all");
              setPickerQuery((match[1] ? match[2] : match[2]).slice(1) || "");
              setPickerOpen(true);
            } else {
              setPickerOpen(false);
            }
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              submit();
            } else if (e.key === "Escape") {
              closePickers();
            }
          }}
        />
        <div className={styles.actionRow}>
          <button
            ref={addButtonRef}
            type="button"
            className={styles.addBtn}
            aria-label="添加资源"
            title="添加资源"
            disabled={disabled}
            onClick={() => setMenuOpen((v) => !v)}
          >
            <Plus size={20} weight="bold" />
          </button>
          <div className={styles.actionEnd}>
            <ModelSettings disabled={disabled || steerMode} onChange={setModelSettings} onValidityChange={setModelSettingsValid} />
            <button
              type="button"
              className={styles.sendBtn}
              disabled={disabled || !modelSettingsValid || (!prompt.trim() && !selectedFileObjs.length && !selectedSkillObjs.length && !selectedToolObjs.length)}
              onClick={submit}
            >
              <PaperPlaneRight size={18} weight="bold" />
              <span>{steerMode ? "发送引导" : "发送"}</span>
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}
