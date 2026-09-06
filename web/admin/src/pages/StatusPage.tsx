import { useCallback, useEffect, useState } from "react";
import { api } from "../api/client";
import {
  AdminClientView,
  AdminPlatformNetworkResponse,
  AdminPlatformWarmPoolResponse,
  AdminRunRow,
} from "../api/types";
import { Badge, ErrorBox, Field, OkBox, fmtTime } from "../components/ui";

export default function StatusPage() {
  const [network, setNetwork] = useState<AdminPlatformNetworkResponse | null>(null);
  const [pool, setPool] = useState<AdminPlatformWarmPoolResponse | null>(null);
  const [clients, setClients] = useState<AdminClientView[]>([]);
  const [error, setError] = useState<unknown>(null);

  const load = useCallback(async () => {
    try {
      const [net, wp, cl] = await Promise.all([
        api<AdminPlatformNetworkResponse>("/platform/network"),
        api<AdminPlatformWarmPoolResponse>("/platform/warm-pool"),
        api<{ clients: AdminClientView[] }>("/clients"),
      ]);
      setNetwork(net);
      setPool(wp);
      setClients(cl.clients);
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
      <div className="panel">
        <h2>平台网络基线</h2>
        <ErrorBox error={error} />
        {network ? (
          <table className="list">
            <tbody>
              <tr>
                <td style={{ width: 180 }} className="muted">active_revision</td>
                <td>{network.active_revision ?? <span className="muted">null（无基线）</span>}</td>
              </tr>
              <tr>
                <td className="muted">active_revision_id</td>
                <td className="mono">{network.active_revision_id ?? "—"}</td>
              </tr>
              <tr>
                <td className="muted">desired_revision_id</td>
                <td className="mono">{network.desired_revision_id ?? "—"}</td>
              </tr>
              <tr>
                <td className="muted">rollout_status</td>
                <td>
                  <Badge value={network.rollout_status || "unknown"} />
                </td>
              </tr>
              <tr>
                <td className="muted">error_summary</td>
                <td>{network.error_summary || "—"}</td>
              </tr>
              <tr>
                <td className="muted">updated_at</td>
                <td className="muted">{fmtTime(network.updated_at)}</td>
              </tr>
            </tbody>
          </table>
        ) : (
          <div className="muted">加载中…</div>
        )}
      </div>

      {pool ? <WarmPoolPanel pool={pool} onSaved={load} /> : null}

      <div className="panel">
        <h2>接入方概览</h2>
        <table className="list">
          <thead>
            <tr>
              <th>名称</th>
              <th>Client ID</th>
              <th>并发配额</th>
              <th>创建时间</th>
              <th>runs</th>
            </tr>
          </thead>
          <tbody>
            {clients.map((c) => (
              <tr key={c.client_id}>
                <td>{c.name}</td>
                <td className="mono">{c.client_id.slice(0, 8)}…</td>
                <td>{c.concurrency_quota}</td>
                <td className="muted">{fmtTime(c.created_at)}</td>
                <td>
                  <RunsPopover clientID={c.client_id} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}

function WarmPoolPanel({ pool, onSaved }: { pool: AdminPlatformWarmPoolResponse; onSaved: () => void }) {
  const [budget, setBudget] = useState(pool.budget ? String(pool.budget) : "");
  const [maxPerImage, setMaxPerImage] = useState(pool.max_per_image ? String(pool.max_per_image) : "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [notice, setNotice] = useState("");

  const save = async () => {
    setBusy(true);
    setError(null);
    setNotice("");
    const body: Record<string, number> = {};
    if (budget !== "") body.budget = Number(budget);
    if (maxPerImage !== "") body.max_per_image = Number(maxPerImage);
    try {
      await api("/platform/warm-pool", { method: "PUT", body });
      setNotice("平台预热池预算已更新");
      onSaved();
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="panel">
      <h2>平台预热池预算 {pool.configured ? <Badge value="configured" /> : <Badge value="未配置（用默认）" />}</h2>
      <div className="hint">
        预算用于限制镜像预热池；镜像 warm_pool_replicas 填 0 表示关闭预热。降低预算导致期望配置不满足返回 409。
      </div>
      <div className="form-row">
        <Field label="budget">
          <input className="field mono" style={{ width: 110 }} type="number" min={0} value={budget} onChange={(e) => setBudget(e.target.value)} />
        </Field>
        <Field label="max_per_image">
          <input className="field mono" style={{ width: 110 }} type="number" min={0} value={maxPerImage} onChange={(e) => setMaxPerImage(e.target.value)} />
        </Field>
        <span className="spacer" />
        <button className="btn primary" disabled={busy} onClick={save}>
          保存预算
        </button>
      </div>
      <ErrorBox error={error} />
      <OkBox text={notice} />
      <div className="muted">当前生效：budget={pool.budget} · max_per_image={pool.max_per_image} · 预热默认关闭</div>
    </div>
  );
}

function RunsPopover({ clientID }: { clientID: string }) {
  const [open, setOpen] = useState(false);
  const [runs, setRuns] = useState<AdminRunRow[] | null>(null);
  const [error, setError] = useState<unknown>(null);

  const toggle = async () => {
    const next = !open;
    setOpen(next);
    if (next && runs === null) {
      try {
        const body = await api<{ runs: AdminRunRow[] }>(`/clients/${clientID}/runs`);
        setRuns(body.runs);
      } catch (err) {
        setError(err);
      }
    }
  };

  return (
    <>
      <button className="btn" onClick={() => void toggle()}>
        {open ? "收起" : "查看"}
      </button>
      {open ? (
        <div className="mt">
          <ErrorBox error={error} />
          {runs === null ? (
            <span className="muted">加载中…</span>
          ) : runs.length === 0 ? (
            <span className="muted">无运行记录</span>
          ) : (
            <table className="list">
              <thead>
                <tr>
                  <th>Run</th>
                  <th>状态</th>
                  <th>Stage</th>
                  <th>创建</th>
                </tr>
              </thead>
              <tbody>
                {runs.slice(0, 10).map((r) => (
                  <tr key={r.run_id}>
                    <td className="mono">{r.run_id.slice(0, 8)}…</td>
                    <td>
                      <Badge value={r.status} />
                    </td>
                    <td>{r.current_stage}</td>
                    <td className="muted">{fmtTime(r.created_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      ) : null}
    </>
  );
}
