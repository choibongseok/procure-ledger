// 변경 웹훅 화면(B42 명세 9.7): 구독(등록·수정·중지·재개·비밀 재발급·시험 보내기·실패 일괄 재전송), 발송 기록(다시 보내기), 주소 검사 설정.
// 구독 관리는 webhook:manage, 발송 기록은 webhook:manage 또는 audit:read. 주소·서명은 화면 어디에도 없고(호스트만) 서명 비밀은 등록·재발급 직후 한 번만 보인다.
import { useState, type FormEvent } from "react";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Badge, ConfirmDialog, EmptyState, Notice, TableWrap, type Tone } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";

interface Sub {
  id: number;
  name: string;
  hint: string;
  events: string[];
  includeApi: boolean;
  active: boolean;
  pausedReason: string;
  state: string;
  failures: number;
  lastOkAt: string;
  lastError: string;
  lastErrorAt: string;
  queued: number;
  failed: number;
  /** 법인이 둘 이상인 설치에서만 온다(B44). */
  entity?: string;
}

interface EventType {
  code: string;
  title: string;
}

interface Delivery {
  id: number;
  subscriptionId: number;
  subscriptionName: string;
  eventSeq: number;
  title: string;
  status: string;
  state: string;
  attempts: number;
  reason: string;
  createdAt: string;
}

interface Hosts {
  allowHosts: string[];
  extraPorts: string[];
}

const STATUS_TONE: Record<string, Tone> = { queued: "info", sending: "info", sent: "ok", failed: "bad", skipped: "muted" };
const fmt = (s?: string) => (s ? new Date(s).toLocaleString("ko-KR") : "-");

interface FormState {
  id: number | null;
  name: string;
  url: string;
  events: string[];
  includeApi: boolean;
}

const emptyForm: FormState = { id: null, name: "", url: "", events: [], includeApi: false };

export function WebhooksPage() {
  const { can, entities = [] } = useAuth();
  // 구독이 묶일 법인(B44): ""=내 범위(그룹 범위가 있으면 전체, 없으면 현재 법인), 숫자=그 법인. 고를 수 있는 법인이 둘 이상일 때만 보인다(새 구독을 만들 때만 정한다).
  const [entityPick, setEntityPick] = useState("");
  const manage = can("webhook:manage");
  const subs = useLoad(() => (manage ? api.get<Sub[]>("/api/admin/integrations/webhooks") : Promise.resolve([] as Sub[])), [manage]);
  const events = useLoad(() => (manage ? api.get<EventType[]>("/api/admin/integrations/webhook-events") : Promise.resolve([] as EventType[])), [manage]);
  const hosts = useLoad(() => (manage ? api.get<Hosts>("/api/admin/integrations/webhook-hosts") : Promise.resolve({ allowHosts: [], extraPorts: [] } as Hosts)), [manage]);
  const [note, setNote] = useState<{ tone: "ok" | "error" | "warn"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [form, setForm] = useState<FormState>(emptyForm);
  const [secret, setSecret] = useState<string | null>(null);
  const [confirm, setConfirm] = useState<{ title: string; text: string; label: string; run: () => Promise<void> } | null>(null);
  const [allowText, setAllowText] = useState<string | null>(null);
  const [portsText, setPortsText] = useState<string | null>(null);
  const [fSub, setFSub] = useState("");
  const [fStatus, setFStatus] = useState("");
  const [fType, setFType] = useState("");
  const [fFrom, setFFrom] = useState("");
  const [fTo, setFTo] = useState("");
  const q = new URLSearchParams();
  if (fSub) q.set("sub", fSub);
  if (fStatus) q.set("status", fStatus);
  if (fType) q.set("type", fType);
  if (fFrom) q.set("from", fFrom);
  if (fTo) q.set("to", fTo);
  const deliveries = useLoad(() => api.get<Delivery[]>(`/api/admin/integrations/webhook-deliveries?${q.toString()}`), [fSub, fStatus, fType, fFrom, fTo]);

  async function run(fn: () => Promise<void>, ok: string, tone: "ok" | "warn" = "ok") {
    setBusy(true);
    setNote(null);
    try {
      await fn();
      if (ok) setNote({ tone, text: ok });
      await Promise.all([subs.reload(), deliveries.reload(), hosts.reload()]);
    } catch (err) {
      setNote({ tone: "error", text: errorText(err) });
    } finally {
      setBusy(false);
    }
  }

  function toggleEvent(code: string) {
    setForm((f) => ({ ...f, events: f.events.includes(code) ? f.events.filter((c) => c !== code) : [...f.events, code] }));
  }

  function edit(s: Sub) {
    setForm({ id: s.id, name: s.name, url: "", events: s.events, includeApi: s.includeApi });
    setSecret(null);
  }

  async function save(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      if (form.id === null) {
        const res = await api.post<{ secret: string }>("/api/admin/integrations/webhooks", { name: form.name, url: form.url, events: form.events, includeApi: form.includeApi, ...(entityPick ? { entityId: Number(entityPick) } : {}) });
        setSecret(res.secret);
      } else {
        const body: Record<string, unknown> = { name: form.name, events: form.events, includeApi: form.includeApi };
        if (form.url.trim()) body.url = form.url;
        await api.put(`/api/admin/integrations/webhooks/${form.id}`, body);
      }
      setForm(emptyForm);
    }, form.id === null ? "구독을 등록했습니다. 서명 비밀은 아래에서 지금만 볼 수 있습니다." : "구독을 수정했습니다.");
  }

  const subList = subs.data ?? [];
  const evTitle = (code: string) => (events.data ?? []).find((x) => x.code === code)?.title ?? code;
  const baseTypes = (events.data ?? []).map((x) => x.code).filter((c) => !c.includes(":"));

  return (
    <section>
      <h1>변경 웹훅</h1>
      <p>
        품목·거래처·발주·입고·전표·채무가 바뀌면 등록한 주소로 서명한 알림을 보냅니다. 알림에는 값이 없고 번호·종류·상태·조회 주소뿐이라, 받는 쪽이 연동 API 토큰으로 값을 읽습니다. 같은 알림이 두 번 올 수 있으니 이벤트 번호로 중복을 걸러야 합니다.
      </p>
      {note && <Notice tone={note.tone}>{note.text}</Notice>}
      {subs.error && <Notice tone="error">{subs.error}</Notice>}

      {manage && (
        <>
          <section className="card" aria-labelledby="wh-form">
            <h2 id="wh-form">{form.id === null ? "구독 등록" : "구독 수정"}</h2>
            <form onSubmit={save} aria-label="구독 양식">
              <div className="field">
                <label htmlFor="wh-name">이름</label>
                <input id="wh-name" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} maxLength={40} required />
              </div>
              <div className="field">
                <label htmlFor="wh-url">{form.id === null ? "받을 주소(https)" : "새 주소(바꿀 때만)"}</label>
                <input id="wh-url" value={form.url} onChange={(e) => setForm({ ...form, url: e.target.value })} required={form.id === null} placeholder="https://" />
                <span className="hint">주소는 저장하면 봉인되어 다시 볼 수 없고 호스트만 표시됩니다.</span>
              </div>
              <fieldset className="field">
                <legend>받을 이벤트</legend>
                <div style={{ columnWidth: 260 }}>
                  {(events.data ?? []).map((ev) => (
                    <div key={ev.code} style={{ breakInside: "avoid" }}>
                      <label>
                        <input type="checkbox" checked={form.events.includes(ev.code)} onChange={() => toggleEvent(ev.code)} /> {ev.title}
                      </label>
                    </div>
                  ))}
                </div>
              </fieldset>
              {entities.length >= 2 && form.id === null && (
                <div className="field">
                  <label htmlFor="wh-entity">법인</label>
                  <select id="wh-entity" value={entityPick} onChange={(e) => setEntityPick(e.target.value)}>
                    <option value="">내 범위(기본)</option>
                    {entities.map((e) => (
                      <option key={e.id} value={e.id}>
                        {e.name}
                      </option>
                    ))}
                  </select>
                  <span className="hint">구독은 고른 법인의 변경만 받습니다. 기본은 내가 볼 수 있는 범위입니다.</span>
                </div>
              )}
              <label>
                <input type="checkbox" checked={form.includeApi} onChange={(e) => setForm({ ...form, includeApi: e.target.checked })} /> 연동 API 가 만든 변경도 받기
              </label>
              <span className="hint"> 끄면 연동이 만든 변경이 되돌아와 서로 계속 쓰는 일을 막습니다(기본 꺼짐).</span>
              <p>
                <button type="submit" disabled={busy || form.events.length === 0}>
                  {form.id === null ? "구독 등록" : "수정 저장"}
                </button>{" "}
                {form.id !== null && (
                  <button type="button" className="btn-secondary" onClick={() => setForm(emptyForm)}>
                    수정 취소
                  </button>
                )}
              </p>
            </form>
            {secret && (
              <div role="status" className="notice notice-warn">
                <p>
                  <strong>서명 비밀은 지금만 볼 수 있습니다.</strong> 받는 쪽의 서명 검증 설정에 붙여 넣으세요. 잃으면 비밀을 재발급합니다.
                </p>
                <code data-testid="webhook-secret">{secret}</code>{" "}
                <button type="button" className="btn-secondary btn-sm" onClick={() => void navigator.clipboard?.writeText(secret)}>
                  복사
                </button>{" "}
                <button type="button" className="btn-sm" onClick={() => setSecret(null)}>
                  저장했습니다
                </button>
              </div>
            )}
          </section>

          <section className="card" aria-labelledby="wh-subs">
            <h2 id="wh-subs">구독</h2>
            {subs.data && subList.length === 0 && <EmptyState hint="위 양식에서 주소와 받을 이벤트를 정해 등록하세요.">등록한 구독이 없습니다.</EmptyState>}
            {subList.length > 0 && (
              <TableWrap>
                <table className="stack-table" aria-label="웹훅 구독">
                  <thead>
                    <tr>
                      <th>이름</th>
                      <th>주소</th>
                      {subList.some((s) => s.entity) && <th>법인</th>}
                      <th>받을 이벤트</th>
                      <th>상태</th>
                      <th>마지막 성공·실패</th>
                      <th>미전달</th>
                      <th />
                    </tr>
                  </thead>
                  <tbody>
                    {subList.map((s) => (
                      <tr key={s.id}>
                        <td data-label="이름">{s.name}</td>
                        <td data-label="주소">{s.hint}</td>
                        {subList.some((x) => x.entity) && <td data-label="법인">{s.entity ?? "—"}</td>}
                        <td data-label="받을 이벤트">{s.events.length}종</td>
                        <td data-label="상태">
                          <Badge tone={s.pausedReason ? "warn" : s.active ? "ok" : "muted"}>{s.state}</Badge>
                          {s.failures > 0 && <span className="hint"> 연속 실패 {s.failures}건</span>}
                        </td>
                        <td data-label="마지막 성공·실패">
                          <div>성공 {fmt(s.lastOkAt)}</div>
                          {s.lastError && (
                            <div className="hint">
                              실패 {fmt(s.lastErrorAt)} · {s.lastError}
                            </div>
                          )}
                        </td>
                        <td data-label="미전달">
                          대기 {s.queued}건 · 실패 {s.failed}건
                        </td>
                        <td>
                          <button type="button" className="btn-secondary btn-sm" disabled={busy} onClick={() => edit(s)}>
                            수정
                          </button>{" "}
                          <button
                            type="button"
                            className="btn-secondary btn-sm"
                            disabled={busy}
                            onClick={() =>
                              void run(async () => {
                                const r = await api.post<{ ok: boolean; message: string }>(`/api/admin/integrations/webhooks/${s.id}/test`);
                                if (!r.ok) throw new Error(r.message || "시험 보내기에 실패했습니다.");
                              }, `「${s.name}」 시험 보내기에 성공했습니다.`)
                            }
                          >
                            시험 보내기
                          </button>{" "}
                          {s.pausedReason ? (
                            <button type="button" className="btn-sm" disabled={busy} onClick={() => void run(async () => void (await api.post(`/api/admin/integrations/webhooks/${s.id}/resume`)), `「${s.name}」 구독을 재개했습니다.`)}>
                              재개
                            </button>
                          ) : (
                            <button type="button" className="btn-secondary btn-sm" disabled={busy} onClick={() => void run(async () => void (await api.post(`/api/admin/integrations/webhooks/${s.id}/pause`)), `「${s.name}」 구독을 중지했습니다.`)}>
                              중지
                            </button>
                          )}{" "}
                          <button
                            type="button"
                            className="btn-secondary btn-sm"
                            disabled={busy}
                            onClick={() =>
                              setConfirm({
                                title: "서명 비밀 재발급",
                                text: `「${s.name}」 구독의 서명 비밀을 새로 만듭니다. 24시간 동안은 옛 비밀과 새 비밀 두 서명을 함께 보내므로, 그 안에 받는 쪽의 비밀을 바꾸세요. 새 비밀은 지금만 볼 수 있습니다.`,
                                label: "재발급",
                                run: async () => {
                                  const r = await api.post<{ secret: string }>(`/api/admin/integrations/webhooks/${s.id}/rotate-secret`);
                                  setSecret(r.secret);
                                },
                              })
                            }
                          >
                            비밀 재발급
                          </button>{" "}
                          {s.failed > 0 && (
                            <button
                              type="button"
                              className="btn-secondary btn-sm"
                              disabled={busy}
                              onClick={() =>
                                void run(async () => {
                                  const r = await api.post<{ count: number }>(`/api/admin/integrations/webhooks/${s.id}/resend-failed`);
                                  setNote({ tone: "ok", text: `실패한 ${r.count}건을 다시 보내기로 돌렸습니다.` });
                                }, "")
                              }
                            >
                              실패 일괄 재전송
                            </button>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </TableWrap>
            )}
          </section>

          <section className="card" aria-labelledby="wh-hosts">
            <h2 id="wh-hosts">주소 검사 설정</h2>
            <p className="hint">허용 호스트를 정하면 그 밖의 주소로는 등록·발송하지 못합니다(비우면 공인 주소는 모두 허용). 443 밖의 포트는 추가해야 쓸 수 있습니다.</p>
            <form
              onSubmit={(e: FormEvent) => {
                e.preventDefault();
                const split = (v: string) => v.split(/[\s,]+/).filter(Boolean);
                void run(
                  async () => void (await api.put("/api/admin/integrations/webhook-hosts", { allowHosts: split(allowText ?? (hosts.data?.allowHosts ?? []).join(", ")), extraPorts: split(portsText ?? (hosts.data?.extraPorts ?? []).join(", ")) })),
                  "주소 검사 설정을 저장했습니다.",
                );
                setAllowText(null);
                setPortsText(null);
              }}
            >
              <div className="field">
                <label htmlFor="wh-allow">허용 호스트(쉼표로 구분)</label>
                <input id="wh-allow" value={allowText ?? (hosts.data?.allowHosts ?? []).join(", ")} onChange={(e) => setAllowText(e.target.value)} placeholder="예: hooks.example.com, *.corp.example" />
              </div>
              <div className="field">
                <label htmlFor="wh-ports">추가 허용 포트(쉼표로 구분)</label>
                <input id="wh-ports" value={portsText ?? (hosts.data?.extraPorts ?? []).join(", ")} onChange={(e) => setPortsText(e.target.value)} placeholder="예: 8443" />
              </div>
              <button type="submit" className="btn-secondary" disabled={busy}>
                설정 저장
              </button>
            </form>
          </section>
        </>
      )}

      <section className="card" aria-labelledby="wh-log">
        <h2 id="wh-log">발송 기록</h2>
        <div className="form-row">
          {manage && (
            <label>
              구독{" "}
              <select value={fSub} onChange={(e) => setFSub(e.target.value)}>
                <option value="">전체</option>
                {subList.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.name}
                  </option>
                ))}
              </select>
            </label>
          )}
          <label>
            상태{" "}
            <select value={fStatus} onChange={(e) => setFStatus(e.target.value)}>
              <option value="">전체</option>
              <option value="queued">대기</option>
              <option value="sent">전달됨</option>
              <option value="failed">실패(포기)</option>
              <option value="skipped">건너뜀</option>
            </select>
          </label>
          {manage && (
            <label>
              이벤트 종류{" "}
              <select value={fType} onChange={(e) => setFType(e.target.value)}>
                <option value="">전체</option>
                {baseTypes.map((c) => (
                  <option key={c} value={c}>
                    {evTitle(c)}
                  </option>
                ))}
              </select>
            </label>
          )}
          <label>
            시작일 <input type="date" value={fFrom} max={fTo || undefined} onChange={(e) => setFFrom(e.target.value)} />
          </label>
          <label>
            종료일 <input type="date" value={fTo} min={fFrom || undefined} onChange={(e) => setFTo(e.target.value)} />
          </label>
        </div>
        {deliveries.error && <Notice tone="error">{deliveries.error}</Notice>}
        {deliveries.data && deliveries.data.length === 0 && <EmptyState hint="변경이 생기고 구독이 있으면 여기에 쌓입니다.">발송 기록이 없습니다.</EmptyState>}
        {deliveries.data && deliveries.data.length > 0 && (
          <TableWrap>
            <table className="stack-table" aria-label="웹훅 발송 기록">
              <thead>
                <tr>
                  <th>시각</th>
                  <th>이벤트</th>
                  <th>구독</th>
                  <th>상태</th>
                  <th>시도</th>
                  <th>사유</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {deliveries.data.map((d) => (
                  <tr key={d.id}>
                    <td data-label="시각" className="nowrap">
                      {fmt(d.createdAt)}
                    </td>
                    <td data-label="이벤트">
                      {d.title} <span className="hint">#{d.eventSeq}</span>
                    </td>
                    <td data-label="구독">{d.subscriptionName}</td>
                    <td data-label="상태">
                      <Badge tone={STATUS_TONE[d.status] ?? "muted"}>{d.state}</Badge>
                    </td>
                    <td data-label="시도">{d.attempts}회</td>
                    <td data-label="사유">{d.reason || "-"}</td>
                    <td>
                      {manage && d.status === "failed" && (
                        <button type="button" className="btn-secondary btn-sm" disabled={busy} onClick={() => void run(async () => void (await api.post(`/api/admin/integrations/webhook-deliveries/${d.id}/resend`)), "다시 보내기로 돌렸습니다.")}>
                          다시 보내기
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        )}
      </section>
      {confirm && (
        <ConfirmDialog
          title={confirm.title}
          confirmLabel={confirm.label}
          busy={busy}
          onConfirm={() => {
            const c = confirm;
            setConfirm(null);
            void run(c.run, "새 서명 비밀을 만들었습니다. 아래에서 지금만 볼 수 있습니다.");
          }}
          onCancel={() => setConfirm(null)}
        >
          <p>{confirm.text}</p>
        </ConfirmDialog>
      )}
    </section>
  );
}
