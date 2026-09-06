import { useCallback, useEffect, useState } from "react";
import { api } from "../api/client";
import {
  AdminClientDetailResponse,
  AdminClientView,
  AdminImageRow,
  AdminRunRow,
  CreateClientResponse,
} from "../api/types";
import { Badge, ErrorBox, OkBox, fmtTime } from "../components/ui";

export default function ClientsPage() {
  const [clients, setClients] = useState<AdminClientView[]>([]);
  const [error, setError] = useState<unknown>(null);
  const [selected, setSelected] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const body = await api<{ clients: AdminClientView[] }>("/clients");
      setClients(body.clients);
      setError(null);
    } catch (err) {
      setError(err);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <>
      <CreateClientPanel onCreated={load} />
      <div className="panel">
        <h2>接入方列表</h2>
        <ErrorBox error={error} />
        <table className="list">
          <thead>
            <tr>
              <th>Client ID</th>
              <th>名称</th>
              <th>并发配额</th>
              <th>创建时间</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {clients.map((c) => (
              <tr key={c.client_id}>
                <td className="mono">{c.client_id}</td>
                <td>{c.name}</td>
                <td>{c.concurrency_quota === 0 ? <span className="muted">未设（0）</span> : c.concurrency_quota}</td>
                <td className="muted">{fmtTime(c.created_at)}</td>
                <td>
                  <button className="btn" onClick={() => setSelected(c.client_id)}>
                    详情
                  </button>
                </td>
              </tr>
            ))}
            {clients.length === 0 ? (
              <tr>
                <td colSpan={5} className="muted">
                  暂无接入方
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>
      {selected ? <ClientDetailPanel clientID={selected} onClose={() => setSelected(null)} /> : null}
    </>
  );
}

function CreateClientPanel({ onCreated }: { onCreated: () => void }) {
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [created, setCreated] = useState<CreateClientResponse | null>(null);

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      const body = await api<CreateClientResponse>("/clients", { method: "POST", body: { name } });
      setCreated(body);
      setName("");
      onCreated();
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="panel">
      <h2>创建接入方</h2>
      <div className="hint">创建成功后 API key 仅在响应中返回一次，请立即保存；平台只存储其 SHA-256 哈希。</div>
      <div className="form-row">
        <input
          className="field"
          placeholder="接入方名称（1–128 字符）"
          value={name}
          maxLength={128}
          onChange={(e) => setName(e.target.value)}
        />
        <button className="btn primary" disabled={busy || !name.trim()} onClick={submit}>
          创建
        </button>
      </div>
      <ErrorBox error={error} />
      {created ? (
        <Modal title={`接入方 ${created.name} 创建成功`} onClose={() => setCreated(null)}>
          <div className="hint">
            请立即复制并保存 API key（关闭后无法再次查看）。Client ID：<span className="mono">{created.client_id}</span>
          </div>
          <div className="key-box">{created.api_key}</div>
          <button className="btn" onClick={() => void navigator.clipboard.writeText(created.api_key)}>
            复制 key
          </button>
        </Modal>
      ) : null}
    </div>
  );
}

export function Modal({ title, children, onClose }: { title: string; children: React.ReactNode; onClose: () => void }) {
  return (
    <div className="modal-mask" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h3>{title}</h3>
        {children}
        <div className="mt">
          <button className="btn primary" onClick={onClose}>
            关闭
          </button>
        </div>
      </div>
    </div>
  );
}

function ClientDetailPanel({ clientID, onClose }: { clientID: string; onClose: () => void }) {
  const [detail, setDetail] = useState<AdminClientDetailResponse | null>(null);
  const [images, setImages] = useState<AdminImageRow[]>([]);
  const [runs, setRuns] = useState<AdminRunRow[]>([]);
  const [error, setError] = useState<unknown>(null);
  const [quota, setQuota] = useState("");
  const [quotaMsg, setQuotaMsg] = useState("");

  useEffect(() => {
    void (async () => {
      try {
        const d = await api<AdminClientDetailResponse>(`/clients/${clientID}`);
        setDetail(d);
        setQuota(String(d.concurrency_quota));
        const [img, run] = await Promise.all([
          api<{ images: AdminImageRow[] }>(`/clients/${clientID}/images`),
          api<{ runs: AdminRunRow[] }>(`/clients/${clientID}/runs`),
        ]);
        setImages(img.images);
        setRuns(run.runs);
      } catch (err) {
        setError(err);
      }
    })();
  }, [clientID]);

  const saveQuota = async () => {
    setQuotaMsg("");
    try {
      await api(`/clients/${clientID}/quota`, {
        method: "PUT",
        body: { concurrency_quota: Number(quota) },
      });
      setQuotaMsg("配额已更新");
    } catch (err) {
      setError(err);
    }
  };

  return (
    <Modal title={`接入方详情 · ${detail?.name ?? clientID}`} onClose={onClose}>
      <ErrorBox error={error} />
      {detail ? (
        <>
          <div className="mono muted">{detail.client_id}</div>
          <div className="section-title">API Keys（仅生命周期信息，平台不暴露明文/哈希）</div>
          <table className="list">
            <thead>
              <tr>
                <th>ID</th>
                <th>标签</th>
                <th>状态</th>
                <th>创建时间</th>
              </tr>
            </thead>
            <tbody>
              {detail.api_keys.map((k) => (
                <tr key={k.id}>
                  <td className="mono">{k.id}</td>
                  <td>{k.label || "—"}</td>
                  <td>
                    <Badge value={k.active ? "enabled" : "disabled"} />
                  </td>
                  <td className="muted">{fmtTime(k.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>

          <div className="section-title">并发配额（PG 权威，0 = 未设）</div>
          <div className="form-row">
            <input
              className="field mono"
              style={{ width: 120 }}
              value={quota}
              onChange={(e) => setQuota(e.target.value)}
            />
            <button className="btn primary" onClick={saveQuota}>
              保存配额
            </button>
            <OkBox text={quotaMsg} />
          </div>

          <div className="section-title">镜像登记（全部状态）</div>
          <table className="list">
            <thead>
              <tr>
                <th>镜像</th>
                <th>状态</th>
                <th>副本</th>
                <th>registration</th>
              </tr>
            </thead>
            <tbody>
              {images.map((img) => (
                <tr key={img.registration_id}>
                  <td className="mono">{img.image_id}</td>
                  <td>
                    <Badge value={img.status} />
                  </td>
                  <td>{img.warm_pool_replicas}</td>
                  <td className="mono muted">{img.registration_id.slice(0, 8)}…</td>
                </tr>
              ))}
              {images.length === 0 ? (
                <tr>
                  <td colSpan={4} className="muted">无镜像登记</td>
                </tr>
              ) : null}
            </tbody>
          </table>

          <div className="section-title">最近 Runs（created_at 倒序，最多 100）</div>
          <table className="list">
            <thead>
              <tr>
                <th>Run</th>
                <th>req_id</th>
                <th>状态</th>
                <th>Stage</th>
                <th>创建</th>
              </tr>
            </thead>
            <tbody>
              {runs.map((r) => (
                <tr key={r.run_id}>
                  <td className="mono">{r.run_id.slice(0, 8)}…</td>
                  <td className="mono muted">{r.req_id.slice(0, 16)}…</td>
                  <td>
                    <Badge value={r.status} />
                  </td>
                  <td>{r.current_stage}</td>
                  <td className="muted">{fmtTime(r.created_at)}</td>
                </tr>
              ))}
              {runs.length === 0 ? (
                <tr>
                  <td colSpan={5} className="muted">无运行记录</td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </>
      ) : (
        <div className="muted">加载中…</div>
      )}
    </Modal>
  );
}
