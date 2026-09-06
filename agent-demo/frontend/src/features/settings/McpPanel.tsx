import {
  DotsThree,
  Link,
  Plus,
  Plugs,
  Trash,
  Wrench,
} from "@phosphor-icons/react";
import { useRef, useState } from "react";
import { createTool, deleteTool, discoverMcpTools, setToolEnabled } from "../../api/tools";
import type { McpDiscoveredTool, Tool } from "../../api/types";
import { Button, EmptyState, FormField } from "../../components";
import Toggle from "./Toggle";
import styles from "./settings.module.css";

interface McpPanelProps {
  tools: Tool[];
  refresh: () => Promise<void>;
}

const isEnabled = (tool: Tool): boolean => {
  const value = tool.enabled as boolean | number | undefined;
  return value === false || value === 0 ? false : true;
};

function McpRow({
  tool,
  onToggle,
  onDelete,
}: {
  tool: Tool;
  onToggle: () => void;
  onDelete: () => void;
}) {
  const [menu, setMenu] = useState(false);
  return (
    <li className={styles.toolRow}>
      <Plugs size={15} className={styles.rowIcon} />
      <div className={styles.toolMain}>
        <div className={styles.toolTop}>
          <span className={styles.rowName}>{tool.name}</span>
          <span className={styles.badge}>HTTP</span>
        </div>
        <code className={styles.toolEndpoint}>{tool.endpoint}</code>
      </div>
      <Toggle
        checked={isEnabled(tool)}
        label={`启用 ${tool.name}`}
        onChange={onToggle}
      />
      <div className={styles.menuWrap}>
        <button
          type="button"
          className={styles.moreBtn}
          aria-label={`管理 ${tool.name}`}
          aria-expanded={menu}
          onClick={(e) => {
            e.stopPropagation();
            setMenu((v) => !v);
          }}
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

export default function McpPanel({ tools, refresh }: McpPanelProps) {
  const nameRef = useRef<HTMLInputElement>(null);
  const [name, setName] = useState("");
  const [desc, setDesc] = useState("");
  const [allowedTools, setAllowedTools] = useState("");
  const [endpoint, setEndpoint] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [saving, setSaving] = useState(false);
  const [fetching, setFetching] = useState(false);
  const [discovered, setDiscovered] = useState<McpDiscoveredTool[]>([]);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [error, setError] = useState<string | null>(null);
  const [ok, setOk] = useState<string | null>(null);

  const mcps = tools.filter((t) => t.type === "mcp");

  const reset = () => {
    setName("");
    setDesc("");
    setAllowedTools("");
    setEndpoint("");
    setEnabled(true);
    setDiscovered([]);
    setSelected(new Set());
    setError(null);
  };

  const save = async () => {
    if (!name.trim() || !endpoint.trim()) {
      setError("请填写工具名称与服务地址");
      return;
    }
    const allowedOperations = allowedTools
      .split(/[\s,，;；]+/)
      .map((item) => item.trim())
      .filter(Boolean);
    if (allowedOperations.length === 0) {
      setError("请填写允许调用的工具列表（allowed_tools）");
      return;
    }
    setSaving(true);
    setError(null);
    setOk(null);
    try {
      await createTool({
        type: "mcp",
        name: name.trim(),
        endpoint: endpoint.trim(),
        allowed_operations: allowedOperations,
        auth: {},
      });
      setOk(`已添加 MCP 工具: ${name.trim()}`);
      reset();
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  const fetchTools = async () => {
    if (!endpoint.trim()) {
      setError("请先填写服务地址，再拉取工具列表");
      return;
    }
    setFetching(true);
    setError(null);
    setOk(null);
    try {
      const list = await discoverMcpTools(endpoint.trim());
      setDiscovered(list);
      setSelected(new Set());
      setOk(`已从服务端拉取到 ${list.length} 个工具，勾选后同步到 allowed_tools`);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setFetching(false);
    }
  };

  const applySelection = (next: Set<string>) => {
    setSelected(next);
    setAllowedTools(Array.from(next).join(", "));
  };

  const toggleDiscovered = (toolName: string, checked: boolean) => {
    const next = new Set(selected);
    if (checked) next.add(toolName);
    else next.delete(toolName);
    applySelection(next);
  };

  const selectAllDiscovered = () =>
    applySelection(new Set(discovered.map((t) => t.name)));

  const clearDiscovered = () => applySelection(new Set());

  const toggle = async (tool: Tool) => {
    try {
      await setToolEnabled(tool.id, !isEnabled(tool));
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  const remove = async (tool: Tool) => {
    if (!window.confirm(`确认删除 MCP 工具 “${tool.name}”？`)) return;
    try {
      await deleteTool(tool.id);
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  const focusName = () => nameRef.current?.focus();

  return (
    <div className={styles.grid}>
      <section className={styles.card} aria-label="已配置的 MCP 工具">
        <div className={styles.cardHeader}>
          <h2>已配置的 MCP 工具</h2>
          <Button size="sm" onClick={focusName}>
            <Plus size={14} />
            添加 MCP
          </Button>
        </div>
        {mcps.length === 0 ? (
          <EmptyState
            compact
            icon={<Wrench size={20} />}
            title="无 MCP 工具"
            description="添加 MCP 工具后，Agent 可调用更多外部能力。"
            action={
              <Button size="sm" onClick={focusName}>
                添加 MCP
              </Button>
            }
          />
        ) : (
          <ul className={styles.list}>
            {mcps.map((tool) => (
              <McpRow
                key={tool.id}
                tool={tool}
                onToggle={() => void toggle(tool)}
                onDelete={() => void remove(tool)}
              />
            ))}
          </ul>
        )}
      </section>

      <section className={styles.card} aria-label="添加 MCP 工具">
        <div className={styles.cardHeader}>
          <h2>添加 MCP 工具</h2>
        </div>
        <div className={styles.form}>
          <FormField label="名称" id="mcp-name" count={{ value: name.length, max: 50 }}>
            <input
              ref={nameRef}
              id="mcp-name"
              className={styles.input}
              placeholder="请输入工具名称"
              maxLength={50}
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </FormField>

          <FormField label="描述 (可选)" id="mcp-desc" count={{ value: desc.length, max: 200 }}>
            <textarea
              id="mcp-desc"
              className={styles.textarea}
              placeholder="请输入工具描述"
              maxLength={200}
              rows={2}
              value={desc}
              onChange={(e) => setDesc(e.target.value)}
            />
          </FormField>

          <FormField label="传输协议" id="mcp-proto">
            <select id="mcp-proto" className={styles.select} disabled value="HTTP">
              <option value="HTTP">HTTP</option>
            </select>
          </FormField>

          <FormField
            label="服务地址"
            id="mcp-url"
            count={{ value: endpoint.length, max: 200 }}
          >
            <input
              id="mcp-url"
              className={styles.input}
              placeholder="请输入 Base URL，例如 https://mcp.example.com"
              maxLength={200}
              value={endpoint}
              onChange={(e) => setEndpoint(e.target.value)}
            />
          </FormField>

          <FormField label="认证方式" id="mcp-auth">
            <select id="mcp-auth" className={styles.select} disabled value="无">
              <option value="无">无</option>
            </select>
          </FormField>

          <div className={styles.discoverRow}>
            <Button
              variant="outline"
              size="sm"
              loading={fetching}
              onClick={() => void fetchTools()}
            >
              从服务端拉取工具列表
            </Button>
          </div>

          <FormField
            label="允许调用的工具（allowed_tools）"
            id="mcp-allowed"
            count={{ value: allowedTools.length, max: 500 }}
          >
            <textarea
              id="mcp-allowed"
              className={styles.textarea}
              placeholder="逗号或换行分隔的工具名，例如：maps_weather, maps_geo, maps_text_search"
              maxLength={500}
              rows={3}
              value={allowedTools}
              onChange={(e) => {
                setAllowedTools(e.target.value);
                setDiscovered([]);
                setSelected(new Set());
              }}
            />
          </FormField>

          {discovered.length > 0 ? (
            <div className={styles.discoverBox}>
              <div className={styles.discoverHeader}>
                <span className={styles.discoverTitle}>
                  服务端工具（已选 {selected.size}/{discovered.length}）
                </span>
                <div className={styles.discoverActions}>
                  <Button variant="outline" size="sm" onClick={selectAllDiscovered}>
                    全选
                  </Button>
                  <Button variant="outline" size="sm" onClick={clearDiscovered}>
                    清空
                  </Button>
                </div>
              </div>
              <ul className={styles.discoverList}>
                {discovered.map((item) => (
                  <li key={item.name} className={styles.discoverItem}>
                    <label className={styles.discoverLabel}>
                      <input
                        type="checkbox"
                        checked={selected.has(item.name)}
                        onChange={(e) =>
                          toggleDiscovered(item.name, e.target.checked)
                        }
                      />
                      <span className={styles.discoverName}>{item.name}</span>
                      <span
                        className={styles.discoverDesc}
                        title={item.description || item.name}
                      >
                        {item.description || "（无描述）"}
                      </span>
                    </label>
                  </li>
                ))}
              </ul>
            </div>
          ) : null}

          <div className={styles.statusRow}>
            <span className={styles.statusLabel}>状态</span>
            <div className={styles.statusControl}>
              <Toggle
                checked={enabled}
                label="启用"
                onChange={setEnabled}
              />
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