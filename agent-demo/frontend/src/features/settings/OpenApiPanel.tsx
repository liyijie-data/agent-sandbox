import {
  DotsThree,
  FileCode,
  Link,
  Scroll,
  Trash,
} from "@phosphor-icons/react";
import { useEffect, useState } from "react";
import { createTool, deleteTool } from "../../api/tools";
import { listOpenApiOperations } from "../../api/resources";
import { pasteOpenApi } from "../../api/resources";
import type { Resource } from "../../api/types";
import type { ResourceKind } from "../../api/types";
import { Button, EmptyState, FormField } from "../../components";
import Toggle from "./Toggle";
import styles from "./settings.module.css";

interface OpenApiPanelProps {
  tools: import("../../api/types").Tool[];
  readySpecs: Resource[];
  upload: (kind: ResourceKind, file: File) => Promise<Resource>;
  refresh: () => Promise<void>;
}


function OpenApiToolRow({
  name,
  onDelete,
}: {
  name: string;
  onDelete: () => void;
}) {
  const [menu, setMenu] = useState(false);

  return (
    <li className={styles.specRow}>
      <FileCode size={15} className={styles.rowIcon} />
      <span className={styles.rowName}>{name}</span>
      <span className={styles.badge}>OpenAPI</span>
      <div className={styles.menuWrap}>
        <button
          type="button"
          className={styles.moreBtn}
          aria-label={`管理 ${name}`}
          aria-expanded={menu}
          onClick={() => setMenu((value) => !value)}
        >
          <DotsThree size={18} />
        </button>
        {menu ? (
          <div className={styles.moreMenu}>
            <button
              type="button"
              className={styles.moreItem}
              onClick={() => {
                setMenu(false);
                onDelete();
              }}
            >
              <Trash size={14} />
              删除
            </button>
          </div>
        ) : null}
      </div>
    </li>
  );
}

export default function OpenApiPanel({
  tools,
  readySpecs,
  refresh,
}: OpenApiPanelProps) {
  const [name, setName] = useState("");
  const [endpoint, setEndpoint] = useState("");
  const [specId, setSpecId] = useState("");
  const [operations, setOperations] = useState<string[]>([]);
  const [selectedOperations, setSelectedOperations] = useState<string[]>([]);
  const [loadingOperations, setLoadingOperations] = useState(false);
  const [enabled, setEnabled] = useState(true);
  const [saving, setSaving] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [ok, setOk] = useState<string | null>(null);
  const [specText, setSpecText] = useState("");
  const [specFormatError, setSpecFormatError] = useState<string | null>(null);

  const inspectSpec = (text: string): string[] => {
    if (!text.trim()) return [];
    try {
      const doc = JSON.parse(text) as Record<string, unknown>;
      if (typeof doc.openapi !== "string") throw new Error("缺少 openapi 版本字段");
      if (!doc.paths || typeof doc.paths !== "object") throw new Error("缺少 paths 接口定义");
      const ids: string[] = [];
      const visit = (value: unknown) => {
        if (value && typeof value === "object") Object.entries(value as Record<string, unknown>).forEach(([key, child]) => {
          if (key === "operationId" && typeof child === "string" && child.trim()) ids.push(child.trim());
          visit(child);
        });
        if (Array.isArray(value)) value.forEach(visit);
      };
      visit(doc.paths);
      return [...new Set(ids)];
    } catch (jsonError) {
      const lines = text.split(/\r?\n/);
      if (!/^\s*openapi\s*:/m.test(text)) throw new Error(jsonError instanceof Error ? `JSON/YAML 格式错误：${jsonError.message}` : "缺少 openapi 版本字段");
      if (!/^\s*paths\s*:/m.test(text)) throw new Error("缺少 paths 接口定义");
      return [...new Set(lines.flatMap((line) => {
        const match = line.match(/^\s*operationId\s*:\s*['\"]?([^'\"#\s]+)['\"]?/);
        return match ? [match[1]] : [];
      }))];
    }
  };

  const previewOperations = (() => { try { return inspectSpec(specText); } catch { return []; } })();
  const handleSpecTextChange = (text: string) => {
    setSpecText(text);
    if (!text.trim()) { setSpecFormatError(null); return; }
    try { inspectSpec(text); setSpecFormatError(null); }
    catch (err) { setSpecFormatError(err instanceof Error ? err.message : String(err)); }
  };

  const openapis = tools.filter((t) => t.type === "openapi");

  useEffect(() => {
    if (!specId) { setOperations([]); return; }
    let cancelled = false;
    setLoadingOperations(true);
    listOpenApiOperations(specId).then((value) => {
      if (!cancelled) { setOperations(value); setSelectedOperations([]); }
    }).catch((err) => {
      if (!cancelled) { setOperations([]); setError(err instanceof Error ? err.message : String(err)); }
    }).finally(() => { if (!cancelled) setLoadingOperations(false); });
    return () => { cancelled = true; };
  }, [specId]);

  const handleSpecPaste = async () => {
    if (!specText.trim()) { setSpecFormatError("请粘贴 JSON 或 YAML 内容"); return; }
    setUploading(true);
    setError(null);
    try {
      const result = await pasteOpenApi("粘贴的 OpenAPI 规范", specText);
      setSpecId(result.id);
      setOperations(result.operations);
      setSelectedOperations([]);
      if (result.server_url) setEndpoint(result.server_url);
      setOk(`已上传规范: ${result.name}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setUploading(false);
    }
  };

  const reset = () => {
    setName("");
    setEndpoint("");
    setEnabled(true);
    setOperations([]);
    setSelectedOperations([]);
    setError(null);
    setSpecText("");
    setSpecFormatError(null);
  };

  const save = async () => {
    if (!name.trim() || !endpoint.trim() || !specId) {
      setError("请填写名称、服务地址并选择一个规范");
      return;
    }
    if (!operations.length) { setError("规范中没有可用的 operationId"); return; }
    if (!selectedOperations.length) { setError("请至少选择一个 operationId"); return; }
    setSaving(true);
    setError(null);
    setOk(null);
    try {
      await createTool({
        type: "openapi",
        name: name.trim(),
        endpoint: endpoint.trim(),
        spec_resource_id: specId,
        allowed_operations: selectedOperations,
      });
      setOk(`已创建 OpenAPI 工具: ${name.trim()}`);
      reset();
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  const remove = async (tool: (typeof openapis)[number]) => {
    if (!window.confirm(`确认删除 OpenAPI 工具 “${tool.name}”？`)) return;
    setError(null);
    try {
      await deleteTool(tool.id);
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  return (
    <div className={styles.grid}>
      <section className={styles.card} aria-label="已配置资源">
        <div className={styles.cardHeader}>
          <h2>已配置的 OpenAPI 工具</h2>
        </div>
        {openapis.length === 0 ? (
          <EmptyState
            compact
            icon={<FileCode size={20} />}
            title="无 OpenAPI 工具"
            description="上传规范并创建工具后，Agent 即可调用对应接口。"
          />
        ) : (
          <ul className={styles.list}>
            {openapis.map((tool) => (
              <OpenApiToolRow
                key={tool.id}
                name={tool.name}
                onDelete={() => void remove(tool)}
              />
            ))}
          </ul>
        )}

        <div className={styles.subHeader}>
          <h3>可用规范</h3>
          <span className={styles.count}>{readySpecs.length}</span>
        </div>
        {readySpecs.length === 0 ? (
          <div className={styles.muted}>尚未上传 JSON/YAML 规范</div>
        ) : (
          <ul className={styles.list}>
            {readySpecs.map((spec) => (
              <li key={spec.id} className={styles.specRow}>
                <Scroll size={15} className={styles.rowIcon} />
                <span className={styles.rowName}>{spec.name}</span>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className={styles.card} aria-label="创建 OpenAPI 工具">
        <div className={styles.cardHeader}>
          <h2>创建 OpenAPI 工具</h2>
        </div>
        <div className={styles.form}>
          <div className={styles.subTitle}>粘贴规范</div>
          <textarea className={styles.textarea} rows={10} value={specText} onChange={(event) => handleSpecTextChange(event.target.value)} placeholder="在这里粘贴 OpenAPI JSON 或 YAML…" />
          {specFormatError ? <div className={styles.error}>{specFormatError}</div> : specText.trim() ? <div className={styles.success}>基础结构有效，发现 {previewOperations.length} 个 operationId</div> : null}
          {previewOperations.length ? <div className={styles.muted}>接口：{previewOperations.join("、")}</div> : specText.trim() && !specFormatError ? <div className={styles.muted}>未发现 operationId，请为每个接口添加 operationId</div> : null}
          <Button variant="outline" size="sm" disabled={uploading || Boolean(specFormatError) || !specText.trim()} onClick={() => void handleSpecPaste()}>{uploading ? "保存中…" : "保存规范"}</Button>

          <div className={styles.subTitle}>绑定服务</div>
          <FormField label="名称" id="oa-name" count={{ value: name.length, max: 50 }}>
            <input
              id="oa-name"
              className={styles.input}
              placeholder="请输入工具名称"
              maxLength={50}
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </FormField>

          <FormField
            label="服务地址"
            id="oa-url"
            count={{ value: endpoint.length, max: 200 }}
          >
            <input
              id="oa-url"
              className={styles.input}
              placeholder="请输入服务地址，例如 https://api.example.com"
              maxLength={200}
              value={endpoint}
              onChange={(e) => setEndpoint(e.target.value)}
            />
          </FormField>

          <FormField label="选择规范" id="oa-spec">
            <select
              id="oa-spec"
              className={styles.select}
              value={specId}
              onChange={(e) => setSpecId(e.target.value)}
            >
              <option value="">选择已上传规范</option>
              {readySpecs.map((spec) => (
                <option key={spec.id} value={spec.id}>
                  {spec.name}
                </option>
              ))}
            </select>
          </FormField>

          <FormField label="允许调用的 operationId" id="oa-operations">
            {loadingOperations ? <div className={styles.muted}>解析规范中…</div> : operations.length === 0 ? (
              <div className={styles.muted}>请选择含 operationId 的 ready 规范</div>
            ) : (
              <div className={styles.form}>
                {operations.map((operation) => (
                  <label key={operation}>
                    <input type="checkbox" checked={selectedOperations.includes(operation)} onChange={(event) => setSelectedOperations((current) => event.target.checked ? [...current, operation] : current.filter((item) => item !== operation))} /> {operation}
                  </label>
                ))}
              </div>
            )}
          </FormField>

          <div className={styles.statusRow}>
            <span className={styles.statusLabel}>状态</span>
            <div className={styles.statusControl}>
              <Toggle checked={enabled} label="启用" onChange={setEnabled} />
              <span>{enabled ? "启用" : "停用"}</span>
            </div>
          </div>

          {error ? <div className={styles.error}>{error}</div> : null}
          {ok ? <div className={styles.success}>{ok}</div> : null}

          <div className={styles.formActions}>
            <Button variant="outline" size="sm" disabled={saving} onClick={reset}>
              取消
            </Button>
            <Button size="sm" loading={saving} onClick={() => void save()}>
              <Link size={14} />
              保存
            </Button>
          </div>
        </div>
      </section>
    </div>
  );
}
