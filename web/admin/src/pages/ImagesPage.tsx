import { useCallback, useEffect, useState } from "react";
import { api } from "../api/client";
import { AdminClientView, AdminImageRow, RegistrationView } from "../api/types";
import { Badge, ErrorBox, Field, OkBox, fmtTime } from "../components/ui";
import { Modal } from "./ClientsPage";

const STATUS_HELP: Record<string, string> = {
  validating: "验证中",
  enabled: "已启用",
  disabled: "已停用（阻止新 Run）",
  revoked: "已吊销（终态，非终态 Run 取消）",
};

export default function ImagesPage() {
  const [clients, setClients] = useState<AdminClientView[]>([]);
  const [clientID, setClientID] = useState("");
  const [images, setImages] = useState<AdminImageRow[]>([]);
  const [error, setError] = useState<unknown>(null);
  const [notice, setNotice] = useState("");
  const [regDetail, setRegDetail] = useState<RegistrationView | null>(null);

  useEffect(() => {
    void (async () => {
      try {
        const body = await api<{ clients: AdminClientView[] }>("/clients");
        setClients(body.clients);
        if (body.clients.length > 0) setClientID((prev) => prev || body.clients[0].client_id);
      } catch (err) {
        setError(err);
      }
    })();
  }, []);

  const loadImages = useCallback(async () => {
    if (!clientID) return;
    try {
      const body = await api<{ images: AdminImageRow[] }>(`/clients/${clientID}/images`);
      setImages(body.images);
      setError(null);
    } catch (err) {
      setError(err);
    }
  }, [clientID]);

  useEffect(() => {
    void loadImages();
  }, [loadImages]);

  const act = async (fn: () => Promise<unknown>, okText: string) => {
    setError(null);
    setNotice("");
    try {
      await fn();
      setNotice(okText);
      await loadImages();
    } catch (err) {
      setError(err);
    }
  };

  const disable = (img: AdminImageRow) =>
    act(
      () => api(`/clients/${clientID}/images/${img.image_id}/disable`, { method: "POST" }),
      `镜像 ${img.image_id} 已停用`,
    );

  const revoke = (img: AdminImageRow) => {
    if (!window.confirm(`吊销 ${img.image_id} 是终态操作且会取消该镜像全部非终态 Run，确认继续？`)) return;
    void act(
      () => api(`/clients/${clientID}/images/${img.image_id}/revoke`, { method: "POST" }),
      `镜像 ${img.image_id} 已吊销`,
    );
  };

  return (
    <>
      <RegisterPanel
        clients={clients}
        clientID={clientID}
        onClientChange={(id) => setClientID(id)}
        onRegistered={() => void loadImages()}
      />
      <div className="panel">
        <h2>镜像登记（{clients.find((c) => c.client_id === clientID)?.name ?? "选择接入方"}）</h2>
        <div className="form-row">
          <select className="field" style={{ width: 320 }} value={clientID} onChange={(e) => setClientID(e.target.value)}>
            {clients.map((c) => (
              <option key={c.client_id} value={c.client_id}>
                {c.name}（{c.client_id.slice(0, 8)}…）
              </option>
            ))}
          </select>
          <button className="btn" onClick={() => void loadImages()}>
            刷新
          </button>
        </div>
        <ErrorBox error={error} />
        <OkBox text={notice} />
        <table className="list">
          <thead>
            <tr>
              <th>镜像</th>
              <th>状态</th>
              <th>digest</th>
              <th>warm_pool 副本</th>
              <th>创建时间</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {images.map((img) => (
              <tr key={img.registration_id}>
                <td className="mono">{img.image_id}</td>
                <td>
                  <Badge value={img.status} />
                </td>
                <td className="mono muted">{img.repository}@{img.digest.slice(0, 19)}…</td>
                <td>{img.warm_pool_replicas}</td>
                <td className="muted">{fmtTime(img.created_at)}</td>
                <td>
                  <div className="row-actions">
                    {img.status !== "enabled" && img.status !== "revoked" ? (
                      <EnableButton
                        digest={img.digest}
                        onEnable={(validationRef) =>
                          act(
                            () =>
                              api(`/clients/${clientID}/images/${img.image_id}/enable`, {
                                method: "POST",
                                body: { digest: img.digest, validation_ref: validationRef },
                              }),
                            `镜像 ${img.image_id} 已启用`,
                          )
                        }
                      />
                    ) : null}
                    {img.status === "enabled" ? (
                      <button className="btn" onClick={() => void disable(img)}>
                        停用
                      </button>
                    ) : null}
                    {img.status !== "revoked" ? (
                      <button className="btn danger" onClick={() => void revoke(img)}>
                        吊销
                      </button>
                    ) : null}
                    <ReplicasButton
                      current={img.warm_pool_replicas}
                      onSave={(n) =>
                        act(
                          () =>
                            api(`/clients/${clientID}/images/${img.image_id}/warm-pool`, {
                              method: "PUT",
                              body: { warm_pool_replicas: n },
                            }),
                          `镜像 ${img.image_id} 副本已设为 ${n}`,
                        )
                      }
                    />
                    <button
                      className="btn"
                      onClick={() =>
                        void api<RegistrationView>(`/registrations/${img.registration_id}`)
                          .then(setRegDetail)
                          .catch(setError)
                      }
                    >
                      验证详情
                    </button>
                  </div>
                </td>
              </tr>
            ))}
            {images.length === 0 ? (
              <tr>
                <td colSpan={6} className="muted">该接入方暂无镜像登记</td>
              </tr>
            ) : null}
          </tbody>
        </table>
        <div className="hint mt">
          {Object.entries(STATUS_HELP).map(([k, v]) => (
            <span key={k} style={{ marginRight: 12 }}>
              <Badge value={k} /> {v}
            </span>
          ))}
        </div>
      </div>
      {regDetail ? (
        <Modal title={`验证详情 · ${regDetail.image_id}`} onClose={() => setRegDetail(null)}>
          <div className="mono muted">{regDetail.registration_id}</div>
          <div className="mt">
            状态 <Badge value={regDetail.status} /> · 阶段 {regDetail.phase}
          </div>
          {regDetail.failure_codes?.length ? (
            <div className="error-box">失败码：{regDetail.failure_codes.join(", ")}</div>
          ) : null}
          <div className="muted mt">digest：<span className="mono">{regDetail.digest}</span></div>
        </Modal>
      ) : null}
    </>
  );
}

function EnableButton({ digest, onEnable }: { digest: string; onEnable: (validationRef: string) => void }) {
  const [open, setOpen] = useState(false);
  const [ref, setRef] = useState("");
  return (
    <>
      <button className="btn primary" onClick={() => setOpen(true)}>
        启用
      </button>
      {open ? (
        <Modal title="启用镜像（绑定验证凭据）" onClose={() => setOpen(false)}>
          <div className="hint">digest（自动绑定）：<span className="mono">{digest.slice(0, 26)}…</span></div>
          <Field label="validation_ref">
            <input className="field mono" value={ref} onChange={(e) => setRef(e.target.value)} placeholder="验收/attestation 记录号" />
          </Field>
          <div className="mt">
            <button
              className="btn primary"
              disabled={!ref.trim()}
              onClick={() => {
                setOpen(false);
                onEnable(ref.trim());
              }}
            >
              确认启用
            </button>
          </div>
        </Modal>
      ) : null}
    </>
  );
}

function ReplicasButton({ current, onSave }: { current: number; onSave: (n: number) => void }) {
  const [open, setOpen] = useState(false);
  const [value, setValue] = useState(String(current));
  return (
    <>
      <button className="btn" onClick={() => { setValue(String(current)); setOpen(true); }}>
        副本
      </button>
      {open ? (
        <Modal title="设置镜像预热池副本（0–512，0 = 关闭预热）" onClose={() => setOpen(false)}>
          <Field label="warm_pool_replicas">
            <input
              className="field mono"
              type="number"
              min={0}
              max={512}
              value={value}
              onChange={(e) => setValue(e.target.value)}
            />
          </Field>
          <div className="mt">
            <button
              className="btn primary"
              onClick={() => {
                setOpen(false);
                onSave(Number(value));
              }}
            >
              保存
            </button>
          </div>
        </Modal>
      ) : null}
    </>
  );
}

function RegisterPanel({
  clients,
  clientID,
  onClientChange,
  onRegistered,
}: {
  clients: AdminClientView[];
  clientID: string;
  onClientChange: (id: string) => void;
  onRegistered: () => void;
}) {
  const [imageID, setImageID] = useState("");
  const [repository, setRepository] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [notice, setNotice] = useState("");

  const submit = async () => {
    setBusy(true);
    setError(null);
    setNotice("");
    try {
      const body = { image_id: imageID.trim(), repository: repository.trim() };
      const resp = await api<{ registration_id: string; status: string }>(`/clients/${clientID}/images`, {
        method: "POST",
        body,
      });
      setNotice(`已提交登记 ${resp.registration_id.slice(0, 8)}… 状态 ${resp.status}`);
      setImageID("");
      setRepository("");
      onRegistered();
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="panel">
      <h2>注册镜像</h2>
      <div className="hint">
        仅填写带 tag 的镜像地址。平台会解析并冻结 OCI manifest digest，自动使用标准 Runtime 契约；提交后仍需启用才可发起 Run。
      </div>
      <div className="form-row">
        <select className="field" style={{ width: 320 }} value={clientID} onChange={(e) => onClientChange(e.target.value)}>
          {clients.map((c) => (
            <option key={c.client_id} value={c.client_id}>
              {c.name}（{c.client_id.slice(0, 8)}…）
            </option>
          ))}
        </select>
      </div>
      <div className="form-grid">
        <Field label="接入方">
          <span className="mono muted">{clientID}</span>
        </Field>
        <span />
        <Field label="image_id">
          <input className="field mono" value={imageID} onChange={(e) => setImageID(e.target.value)} placeholder="如 my-agent-v1" />
        </Field>
        <span />
        <Field label="repository（含 tag）">
          <input className="field mono" value={repository} onChange={(e) => setRepository(e.target.value)} placeholder="registry.example/team/image:tag" />
        </Field>
        <span />
      </div>
      <ErrorBox error={error} />
      <OkBox text={notice} />
      <button className="btn primary" disabled={busy || !clientID || !imageID.trim() || !repository.trim()} onClick={submit}>
        {busy ? "提交中…" : "注册"}
      </button>
    </div>
  );
}
