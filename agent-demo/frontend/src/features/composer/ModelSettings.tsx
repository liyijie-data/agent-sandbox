import { useState } from "react";
import styles from "./composer.module.css";

export interface ModelSettingsValue {
  reasoningEffort?: string;
  contextWindowTokens?: number;
  maxOutputTokens?: number;
  modelParameters?: Record<string, unknown>;
}

interface Props {
  disabled: boolean;
  onChange: (value: ModelSettingsValue) => void;
  onValidityChange: (valid: boolean) => void;
}

const PROTECTED = new Set([
  "model", "messages", "tools", "stream", "n", "base_url", "access",
  "token", "api_key", "authorization", "headers", "context_window_tokens",
  "max_output_tokens",
]);

function validateJson(raw: string, effort: string): { value?: Record<string, unknown>; error?: string } {
  if (!raw.trim()) return {};
  let value: unknown;
  try { value = JSON.parse(raw); } catch { return { error: "通用参数必须是有效 JSON 对象" }; }
  if (!value || typeof value !== "object" || Array.isArray(value)) return { error: "通用参数必须是 JSON 对象" };
  let nodes: Array<{ value: unknown; depth: number }> = [{ value, depth: 1 }];
  while (nodes.length) {
    const current = nodes.pop()!;
    if (current.depth > 32) return { error: "通用参数嵌套不能超过 32 层" };
    if (typeof current.value === "number" && (!Number.isFinite(current.value) || (Number.isInteger(current.value) && !Number.isSafeInteger(current.value)))) return { error: "通用参数不能包含非安全整数；大整数请通过 API 提交" };
    if (current.value && typeof current.value === "object") {
      for (const [key, child] of Object.entries(current.value)) {
        if (current.depth === 1 && PROTECTED.has(key)) return { error: `通用参数包含运行时保留字段：${key}` };
        nodes.push({ value: child, depth: current.depth + 1 });
      }
    }
  }
  const size = new TextEncoder().encode(JSON.stringify(value)).length;
  if (size > 64 * 1024) return { error: "通用参数不能超过 64 KiB" };
  const obj = value as Record<string, unknown>;
  const aliases = ["max_tokens", "max_completion_tokens"].filter((key) => Object.prototype.hasOwnProperty.call(obj, key));
  if (aliases.length > 1) return { error: "max_tokens 与 max_completion_tokens 只能传一个" };
  for (const key of aliases) {
    if (!Number.isInteger(obj[key]) || (obj[key] as number) <= 0 || (obj[key] as number) > 2097152) return { error: `${key} 必须是 1 到 2097152 的整数` };
  }
  if (Object.prototype.hasOwnProperty.call(obj, "reasoning_effort")) {
    if (obj.reasoning_effort !== null && (typeof obj.reasoning_effort !== "string" || !obj.reasoning_effort.trim())) return { error: "reasoning_effort 必须是非空字符串或 null" };
    if (effort && obj.reasoning_effort !== effort) return { error: "reasoning_effort 与思考强度冲突" };
  }
  return { value: value as Record<string, unknown> };
}

export default function ModelSettings({ disabled, onChange, onValidityChange }: Props) {
  const [open, setOpen] = useState(false);
  const [effort, setEffort] = useState("");
  const [context, setContext] = useState("");
  const [output, setOutput] = useState("");
  const [raw, setRaw] = useState("");
  const [error, setError] = useState<string | null>(null);

  const update = (next: Partial<{ effort: string; context: string; output: string; raw: string }>) => {
    const nextEffort = next.effort ?? effort;
    const nextContext = next.context ?? context;
    const nextOutput = next.output ?? output;
    const nextRaw = next.raw ?? raw;
    const parsed = validateJson(nextRaw, nextEffort);
    const fail = (message: string) => { setError(message); setOpen(true); onValidityChange(false); };
    if (parsed.error) { fail(parsed.error); return; }
    const contextValue = nextContext.trim() ? Number(nextContext) : undefined;
    const outputValue = nextOutput.trim() ? Number(nextOutput) : undefined;
    if (contextValue !== undefined && (!Number.isInteger(contextValue) || !Number.isSafeInteger(contextValue) || contextValue <= 0 || contextValue > 2097152)) { fail("上下文 token 必须是 1 到 2097152 的整数"); return; }
    if (outputValue !== undefined && (!Number.isInteger(outputValue) || !Number.isSafeInteger(outputValue) || outputValue <= 0 || outputValue > 2097152)) { fail("输出 token 必须是 1 到 2097152 的整数"); return; }
    if (contextValue !== undefined && outputValue !== undefined && contextValue <= outputValue) { fail("上下文 token 必须大于输出 token"); return; }
    const parameterLimit = parsed.value?.max_tokens ?? parsed.value?.max_completion_tokens;
    if (parameterLimit !== undefined && outputValue !== undefined && parameterLimit !== outputValue) { fail("通用参数 token 上限与输出 token 冲突"); return; }
    if (parameterLimit !== undefined && contextValue !== undefined && contextValue <= (parameterLimit as number)) { fail("上下文 token 必须大于通用参数 token 上限"); return; }
    setError(null);
    onValidityChange(true);
    onChange({ reasoningEffort: nextEffort || undefined, contextWindowTokens: contextValue, maxOutputTokens: outputValue, modelParameters: parsed.value });
  };
  return <div className={styles.modelSettings}>
    <button type="button" className={styles.settingsToggle} disabled={disabled} onClick={() => setOpen((value) => !value)}>模型设置 {open ? "⌃" : "⌄"}</button>
    {open ? <div className={styles.settingsPanel}>
      <label>思考强度<select value={effort} disabled={disabled} onChange={(e) => { setEffort(e.target.value); update({ effort: e.target.value }); }}><option value="">默认</option><option value="low">快速</option><option value="medium">标准</option><option value="high">深度</option></select></label>
      <label>上下文 token<input inputMode="numeric" placeholder="默认 262144（256K）" value={context} disabled={disabled} onChange={(e) => { setContext(e.target.value); update({ context: e.target.value }); }} /></label>
      <label>输出 token<input inputMode="numeric" placeholder="默认 4096" value={output} disabled={disabled} onChange={(e) => { setOutput(e.target.value); update({ output: e.target.value }); }} /></label>
      <label>通用模型参数<textarea placeholder={'例如 {"temperature":0.3,"top_p":0.9}'} value={raw} disabled={disabled} onChange={(e) => { setRaw(e.target.value); update({ raw: e.target.value }); }} /></label>
      <small>参数会按 OpenAI-compatible Chat Completions 请求透传；空值使用 Runtime 默认。</small>
    </div> : null}
    {error ? <small className={styles.settingsError} role="alert">{error}</small> : null}
  </div>;
}
