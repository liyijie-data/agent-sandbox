import { BookOpen, CloudArrowUp, Sparkle } from "@phosphor-icons/react";
import { useRef, useState } from "react";
import type { Resource, ResourceKind } from "../../api/types";
import { formatBytes } from "../../lib/format";
import { Button, EmptyState } from "../../components";
import styles from "./settings.module.css";

interface SkillsPanelProps {
  readySkills: Resource[];
  upload: (kind: ResourceKind, file: File) => Promise<Resource>;
}

export default function SkillsPanel({ readySkills, upload }: SkillsPanelProps) {
  const fileRef = useRef<HTMLInputElement>(null);
  const [uploading, setUploading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);

  const pickFile = () => fileRef.current?.click();

  const handleFile = async (file: File | undefined) => {
    if (!file) return;
    setUploading(true);
    setError(null);
    setDone(null);
    try {
      const result = await upload("skills", file);
      setDone(`已上传并解析: ${result.name}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setUploading(false);
      if (fileRef.current) fileRef.current.value = "";
    }
  };

  return (
    <div className={styles.grid}>
      <section className={styles.card} aria-label="已就绪 Skill">
        <div className={styles.cardHeader}>
          <h2>已就绪 Skill</h2>
          <span className={styles.count}>{readySkills.length}</span>
        </div>
        {readySkills.length === 0 ? (
          <EmptyState
            compact
            icon={<Sparkle size={20} />}
            title="暂无 Skill"
            description="上传 Skill 压缩包后，Agent 即可按需调度该能力。"
            action={
              <Button size="sm" variant="outline" onClick={pickFile}>
                上传 Skill
              </Button>
            }
          />
        ) : (
          <ul className={styles.list}>
            {readySkills.map((skill) => (
              <li key={skill.id} className={styles.listRow}>
                <BookOpen size={15} className={styles.rowIcon} />
                <span className={styles.rowName}>{skill.name}</span>
                {skill.version ? (
                  <span className={styles.rowVersion}>v{skill.version}</span>
                ) : null}
                <span className={styles.rowMeta}>{formatBytes(skill.size_bytes)}</span>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className={styles.card} aria-label="上传 Skill">
        <div className={styles.cardHeader}>
          <h2>上传 Skill</h2>
        </div>
        <input
          ref={fileRef}
          type="file"
          accept=".zip"
          className={styles.hiddenInput}
          onChange={(e) => void handleFile(e.target.files?.[0])}
        />
        <p className={styles.hint}>
          上传一个 .zip 压缩包，其中包含 SKILL.md 与所需资源。
        </p>
        <button
          type="button"
          className={styles.dropzone}
          disabled={uploading}
          onClick={pickFile}
        >
          <CloudArrowUp size={22} />
          <span className={styles.dropzoneTitle}>
            {uploading ? "上传中…" : "选择 .zip 文件并上传"}
          </span>
          <span className={styles.dropzoneHint}>支持 SKILL.zip 格式</span>
        </button>
        {error ? <div className={styles.error}>{error}</div> : null}
        {done ? <div className={styles.success}>{done}</div> : null}
      </section>
    </div>
  );
}