import { DownloadSimple, FileText } from "@phosphor-icons/react";
import type { Artifact } from "../../api/types";
import { baseName } from "../../lib/format";
import styles from "./chat.module.css";

export default function ArtifactLink({
  artifact,
  onDownload,
}: {
  artifact: Artifact;
  onDownload: (item: Artifact) => void;
}) {
  const name = baseName(artifact.name);
  return (
    <button
      type="button"
      className={styles.artifactLink}
      onClick={() => onDownload(artifact)}
      title={`下载 ${name}`}
    >
      <FileText size={15} className={styles.artifactIcon} />
      <span className={styles.artifactName}>{name}</span>
      <DownloadSimple size={14} className={styles.artifactDownload} />
    </button>
  );
}