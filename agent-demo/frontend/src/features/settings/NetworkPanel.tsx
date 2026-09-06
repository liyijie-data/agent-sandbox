import { ArrowClockwise, Plus, Trash } from "@phosphor-icons/react";
import { useEffect, useState } from "react";
import {
  getImageNetworkConfig,
  getNetworkConfig,
  putImageNetworkConfig,
  putNetworkConfig,
} from "../../api/platform";
import type { EgressRule, HostAlias, NetworkConfigResponse } from "../../api/types";
import { Button, FormField } from "../../components";
import styles from "./settings.module.css";

const DEFAULT_IMAGE_ID = "agent-platform-runtime-v1";

const newReqId = (): string =>
  `cfg-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;

function parsePorts(text: string): Array<{ protocol: string; port: number }> {
  const ports: Array<{ protocol: string; port: number }> = [];
  for (const raw of text.split(/[,，\s]+/)) {
    const port = Number(raw);
    if (Number.isInteger(port) && port >= 1 && port <= 65535) {
      ports.push({ protocol: "TCP", port });
    }
  }
  return ports;
}

interface EditorProps {
  title: string;
  subtitle: string;
  imageId?: string;
}

function NetworkConfigEditor({ title, subtitle, imageId }: EditorProps) {
  const [config, setConfig] = useState<NetworkConfigResponse | null>(null);
  const [hostAliases, setHostAliases] = useState<HostAlias[]>([]);
  const [egress, setEgress] = useState<EgressRule[]>([]);
  const [portsText, setPortsText] = useState<string[]>([]);
  const [expectedRevision, setExpectedRevision] = useState("");
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [ok, setOk] = useState<string | null>(null);

  const load = async () => {
    setLoading(true);
    setError(null);
    setOk(null);
    try {
      const value = imageId
        ? await getImageNetworkConfig(imageId)
        : await getNetworkConfig();
      setConfig(value);
      setHostAliases(Array.isArray(value.host_aliases) ? value.host_aliases : []);
      const rules = Array.isArray(value.network_policy?.egress)
        ? value.network_policy.egress
        : [];
      setEgress(rules);
      setPortsText(
        rules.map((rule) =>
          (rule.ports ?? [])
            .map((p) => p.port)
            .filter((port) => Number.isInteger(port))
            .join(","),
        ),
      );
      setExpectedRevision(!imageId || value.source === "image" ? value.revision_id ?? "" : "");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [imageId]);

  const patchAlias = (index: number, field: keyof HostAlias, value: string) =>
    setHostAliases((prev) =>
      prev.map((item, i) => (i === index ? { ...item, [field]: value } : item)),
    );

  const patchEgress = (index: number, field: keyof EgressRule, value: string) =>
    setEgress((prev) =>
      prev.map((item, i) =>
        i === index
          ? field === "cidr"
            ? { ...item, cidr: value }
            : { ...item, ports: parsePorts(value) }
          : item,
      ),
    );

  const save = async () => {
    const validAliases = hostAliases.filter((a) => a.hostname.trim() && a.ip.trim());
    const payload = {
      req_id: newReqId(),
      host_aliases: validAliases,
      network_policy: {
        egress: egress.map((rule, index) => ({
          cidr: rule.cidr.trim(),
          ports: parsePorts(portsText[index] ?? ""),
        })),
      },
      ...(expectedRevision.trim()
        ? { expected_active_revision: expectedRevision.trim() }
        : {}),
    };
    setSaving(true);
    setError(null);
    setOk(null);
    try {
      const value = imageId
        ? await putImageNetworkConfig(imageId, payload)
        : await putNetworkConfig(payload);
      setConfig(value);
      setOk(
        `已提交（revision ${value.revision}，rollout ${value.rollout_status}${
          value.revision_id ? `，revision_id ${value.revision_id}` : ""
        }）`,
      );
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  return (
    <section className={styles.card} aria-label={title}>
      <div className={styles.cardHeader}>
        <div>
          <h2>{title}</h2>
          <p className={styles.hint}>{subtitle}</p>
        </div>
        <Button size="sm" variant="outline" loading={loading} onClick={() => void load()}>
          <ArrowClockwise size={14} />
          刷新
        </Button>
      </div>

      {config ? (
        <div className={styles.metaLine}>
          生效来源 <span className={styles.badge}>{config.source}</span>
          <span className={styles.metaSeparator}>·</span> revision {config.revision}
          {config.revision_id ? (
            <>
              <span className={styles.metaSeparator}>·</span>
              <code className={styles.revisionId}>{config.revision_id}</code>
            </>
          ) : null}
          <span className={styles.metaSeparator}>·</span> rollout{" "}
          <span className={styles.badge}>{config.rollout_status}</span>
          {config.scope === "image" && imageId ? (
            <>
              <span className={styles.metaSeparator}>·</span> 镜像 {imageId} 的 override
            </>
          ) : null}
        </div>
      ) : null}

      <div className={styles.form}>
        <div className={styles.subHeader}>
          <h3>host_aliases（仅写 /etc/hosts，不授权网络；alias IP 需被同一配置 egress 覆盖）</h3>
        </div>
        {hostAliases.length === 0 ? (
          <p className={styles.muted}>暂无 host_aliases。</p>
        ) : (
          hostAliases.map((alias, index) => (
            <div key={index} className={styles.aliasRow}>
              <input
                className={styles.input}
                placeholder="hostname，如 db.internal"
                value={alias.hostname}
                onChange={(e) => patchAlias(index, "hostname", e.target.value)}
              />
              <input
                className={styles.input}
                placeholder="IP，如 203.0.113.10"
                value={alias.ip}
                onChange={(e) => patchAlias(index, "ip", e.target.value)}
              />
              <Button
                size="sm"
                variant="ghost"
                aria-label={`删除 host_alias ${index + 1}`}
                onClick={() =>
                  setHostAliases((prev) => prev.filter((_, i) => i !== index))
                }
              >
                <Trash size={14} />
              </Button>
            </div>
          ))
        )}
        <div>
          <Button
            size="sm"
            variant="outline"
            onClick={() => setHostAliases((prev) => [...prev, { hostname: "", ip: "" }])}
          >
            <Plus size={14} />
            添加 host_alias
          </Button>
        </div>

        <div className={styles.subHeader}>
          <h3>network_policy.egress（cidr + 端口列表）</h3>
        </div>
        {egress.length === 0 ? (
          <p className={styles.muted}>暂无 egress 规则。</p>
        ) : (
          egress.map((rule, index) => (
            <div key={index} className={styles.aliasRow}>
              <input
                className={styles.input}
                placeholder="CIDR，如 203.0.113.0/24"
                value={rule.cidr}
                onChange={(e) => patchEgress(index, "cidr", e.target.value)}
              />
              <input
                className={styles.input}
                placeholder="TCP 端口，如 5432,9000"
                value={portsText[index] ?? ""}
                onChange={(e) => {
                  setPortsText((prev) => {
                    const next = [...prev];
                    next[index] = e.target.value;
                    return next;
                  });
                  patchEgress(index, "ports", e.target.value);
                }}
              />
              <Button
                size="sm"
                variant="ghost"
                aria-label={`删除 egress 规则 ${index + 1}`}
                onClick={() =>
                  setEgress((prev) => prev.filter((_, i) => i !== index))
                }
              >
                <Trash size={14} />
              </Button>
            </div>
          ))
        )}
        <div>
          <Button
            size="sm"
            variant="outline"
            onClick={() =>
              setEgress((prev) => [...prev, { cidr: "", ports: [] }])
            }
          >
            <Plus size={14} />
            添加 egress 规则
          </Button>
        </div>

        <FormField
          label="expected_active_revision（CAS）"
          hint="已预填当前激活 revision_id 做乐观并发控制；不匹配时平台返回 409 config_state_conflict。留空表示预期当前无激活修订，仅在首次配置（或已被清除）时可提交。"
        >
          <input
            className={styles.input}
            placeholder="留空=预期当前无激活修订"
            value={expectedRevision}
            onChange={(e) => setExpectedRevision(e.target.value)}
          />
        </FormField>

        {error ? <div className={styles.error}>{error}</div> : null}
        {ok ? <div className={styles.success}>{ok}</div> : null}

        <div className={styles.formActions}>
          <Button size="sm" loading={saving} onClick={() => void save()}>
            提交（完整替换）
          </Button>
        </div>
      </div>
    </section>
  );
}

export default function NetworkPanel() {
  const [imageId, setImageId] = useState(DEFAULT_IMAGE_ID);

  return (
    <div className={styles.stack}>
      <NetworkConfigEditor
        title="Client 全局网络配置"
        subtitle="GET/PUT /api/v1/config/network — 所有镜像的默认配置；全局更新会清除单镜像 override。"
      />
      <NetworkConfigEditor
        title="镜像 override"
        subtitle="GET/PUT /api/v1/images/{image_id}/config/network — 只影响目标镜像。"
        imageId={imageId}
      />
      <div className={styles.metaLine}>
        镜像 override 目标：
        <input
          className={styles.input}
          style={{ maxWidth: 320 }}
          value={imageId}
          onChange={(e) => setImageId(e.target.value)}
        />
      </div>
    </div>
  );
}
