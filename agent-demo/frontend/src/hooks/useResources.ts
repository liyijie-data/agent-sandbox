import { useCallback, useState } from "react";
import {
  listResources,
  uploadResource,
} from "../api/resources";
import { listTools } from "../api/tools";
import type { Resource, ResourceKind, Tool } from "../api/types";

export function useResources() {
  const [files, setFiles] = useState<Resource[]>([]);
  const [skills, setSkills] = useState<Resource[]>([]);
  const [specs, setSpecs] = useState<Resource[]>([]);
  const [tools, setTools] = useState<Tool[]>([]);
  const [selectedFiles, setSelectedFiles] = useState<string[]>([]);
  const [selectedSkills, setSelectedSkills] = useState<string[]>([]);
  const [selectedTools, setSelectedTools] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const loadAll = useCallback(async () => {
    setLoading(true);
    setError(null);
    const results = await Promise.allSettled([
      listResources("files"),
      listResources("skills"),
      listResources("openapi"),
      listTools(),
    ]);
    if (results[0].status === "fulfilled") setFiles(results[0].value);
    if (results[1].status === "fulfilled") setSkills(results[1].value);
    if (results[2].status === "fulfilled") setSpecs(results[2].value);
    if (results[3].status === "fulfilled") setTools(results[3].value);
    const failed = results.find((r) => r.status === "rejected");
    if (failed) setError("部分资源加载失败");
    setLoading(false);
  }, []);

  const toggleIn = useCallback(
    (id: string, values: string[], setter: (x: string[]) => void) => {
      setter(values.includes(id) ? values.filter((x) => x !== id) : [...values, id]);
    },
    [],
  );

  const toggleFile = useCallback(
    (id: string) => toggleIn(id, selectedFiles, setSelectedFiles),
    [selectedFiles, toggleIn],
  );
  const toggleSkill = useCallback(
    (id: string) => toggleIn(id, selectedSkills, setSelectedSkills),
    [selectedSkills, toggleIn],
  );
  const toggleTool = useCallback(
    (id: string) => toggleIn(id, selectedTools, setSelectedTools),
    [selectedTools, toggleIn],
  );
  const removeFile = useCallback(
    (id: string) => setSelectedFiles((x) => x.filter((v) => v !== id)),
    [],
  );
  const removeSkill = useCallback(
    (id: string) => setSelectedSkills((x) => x.filter((v) => v !== id)),
    [],
  );
  const removeTool = useCallback(
    (id: string) => setSelectedTools((x) => x.filter((v) => v !== id)),
    [],
  );

  const clearSelection = useCallback(() => {
    setSelectedFiles([]);
    setSelectedSkills([]);
    setSelectedTools([]);
  }, []);

  const upload = useCallback(
    async (kind: ResourceKind, file: File, version?: string) => {
      const result = await uploadResource(kind, file, version);
      if (kind === "files") {
        setFiles((x) => [result, ...x]);
        setSelectedFiles((x) => (x.includes(result.id) ? x : [...x, result.id]));
      } else if (kind === "skills") {
        setSkills((x) => [result, ...x]);
      } else {
        setSpecs((x) => [result, ...x]);
      }
      return result;
    },
    [],
  );

  const readySkills = skills.filter((s) => s.status === "ready");
  const readySpecs = specs.filter((s) => s.status === "ready");

  return {
    files,
    skills,
    specs,
    tools,
    selectedFiles,
    selectedSkills,
    selectedTools,
    loading,
    error,
    loadAll,
    upload,
    toggleFile,
    toggleSkill,
    toggleTool,
    removeFile,
    removeSkill,
    removeTool,
    clearSelection,
    readySkills,
    readySpecs,
  };
}