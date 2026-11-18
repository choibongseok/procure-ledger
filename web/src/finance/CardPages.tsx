// 법인카드 화면(B32 3.3): 내 카드 내역·소명, 소명 확인(조직장), 카드 관리·업로드·재무 현황. 카드번호는 끝 4자리만 다루며 화면에는 별칭만 보인다.
import { useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Badge, ConfirmDialog, EmptyState, Notice, TableWrap } from "../layout/ui";
import { dateTime, won } from "../purchase/labels";
import { errorText, useLoad } from "../purchase/useLoad";
import { CARD_STATUS_CHOICES, CARD_STATUS_TONE, VAT_REASONS, type Card, type CardOverview, type CardTxn, type ImportPreview } from "./cardTypes";

const kstToday = () => new Date(Date.now() + 9 * 3600 * 1000).toISOString().slice(0, 10);

export function CardStatusBadge({ t }: { t: Pick<CardTxn, "status" | "statusLabel"> }) {
  return <Badge tone={CARD_STATUS_TONE[t.status]}>{t.statusLabel}</Badge>;
}

function DueBadge({ t }: { t: CardTxn }) {
  if (t.status !== "unreconciled" && t.status !== "needs_fix") return null;
  if (t.overdue) return <Badge tone="bad">기한 {-t.daysLeft}일 경과</Badge>;
  return <Badge tone={t.daysLeft <= 1 ? "warn" : "muted"}>기한까지 {t.daysLeft}일</Badge>;
}

// ---- 내 카드 내역·소명 ----

export function CardsMinePage() {
  const [status, setStatus] = useState("");
  const [tick, setTick] = useState(0);
  const list = useLoad(() => api.get<CardTxn[]>(`/api/finance/cards/mine${status ? `?status=${status}` : ""}`), [status, tick]);
  const accounts = useLoad(() => api.get<{ code: string; name: string }[]>("/api/finance/cards/explain-accounts"), []);
  const [open, setOpen] = useState(0);
  const [form, setForm] = useState({ purpose: "", accountCode: "", deduct: true, vatReason: "entertainment", personal: false });
  const [files, setFiles] = useState<{ id: number; name: string }[]>([]);
  const [msg, setMsg] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const rows = list.data ?? [];

  async function upload(file: File) {
    setMsg(null);
    try {
      const fd = new FormData();
      fd.append("file", file);
      const a = await api.post<{ id: number; filename: string }>("/api/approval-docs/attachments", fd);
      setFiles((f) => [...f, { id: a.id, name: a.filename }]);
    } catch (e) {
      setMsg({ tone: "error", text: errorText(e) });
    }
  }
  async function submit(t: CardTxn) {
    setBusy(true);
    setMsg(null);
    try {
      await api.post(`/api/finance/cards/mine/${t.id}/explain`, {
        purpose: form.purpose,
        accountCode: form.personal ? "" : form.accountCode,
        vatDeduct: form.deduct,
        vatReason: form.deduct ? "" : form.vatReason,
        personal: form.personal,
        attachmentIds: files.map((f) => f.id),
      });
      setMsg({ tone: "ok", text: "소명을 제출했습니다. 소속 조직장이 확인합니다." });
      setOpen(0);
      setFiles([]);
      setForm({ purpose: "", accountCode: "", deduct: true, vatReason: "entertainment", personal: false });
      setTick((x) => x + 1);
    } catch (e) {
      setMsg({ tone: "error", text: errorText(e) });
    } finally {
      setBusy(false);
    }
  }
  async function withdraw(t: CardTxn) {
    setMsg(null);
    try {
      await api.post(`/api/finance/cards/mine/${t.id}/withdraw`);
      setMsg({ tone: "ok", text: "소명을 철회했습니다. 다시 소명할 수 있습니다." });
      setTick((x) => x + 1);
    } catch (e) {
      setMsg({ tone: "error", text: errorText(e) });
    }
  }

  return (
    <section>
      <h1>내 카드 내역</h1>
      <p className="hint">사용 내역마다 용도·계정과목·영수증을 적어 소명하세요. 3만원을 넘는 사용 내역은 영수증을 첨부해야 합니다.</p>
      <div className="form-row">
        <div className="field">
          <label htmlFor="cm-status">상태</label>
          <select id="cm-status" value={status} onChange={(e) => setStatus(e.target.value)}>
            {CARD_STATUS_CHOICES.map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </select>
        </div>
      </div>
      {msg && <Notice tone={msg.tone}>{msg.text}</Notice>}
      {list.error && <Notice tone="error">{list.error}</Notice>}
      {list.loading && <p>불러오는 중…</p>}
      {list.data && rows.length === 0 && <EmptyState>소명할 내역이 없습니다.</EmptyState>}
      {rows.length > 0 && (
        <TableWrap>
          <table className="stack-table">
            <thead>
              <tr>
                <th className="nowrap">사용일</th>
                <th>가맹점</th>
                <th>카드</th>
                <th className="num nowrap">금액</th>
                <th className="nowrap">소명 기한</th>
                <th className="nowrap">상태</th>
                <th>처리</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((t) => (
                <tr key={t.id}>
                  <td data-label="사용일" className="nowrap">{dateTime(t.usedAt)}</td>
                  <td data-label="가맹점">{t.merchant}</td>
                  <td data-label="카드">{t.cardAlias}</td>
                  <td data-label="금액" className="num nowrap">{won(t.amount)}</td>
                  <td data-label="소명 기한" className="nowrap">
                    {t.dueDate} <DueBadge t={t} />
                  </td>
                  <td data-label="상태" className="nowrap">
                    <CardStatusBadge t={t} />
                  </td>
                  <td data-label="처리">
                    {(t.status === "unreconciled" || t.status === "needs_fix") && t.kind === "approve" && (
                      <button type="button" onClick={() => setOpen(open === t.id ? 0 : t.id)}>
                        소명하기
                      </button>
                    )}
                    {t.status === "submitted" && (
                      <button type="button" className="btn-secondary" onClick={() => void withdraw(t)}>
                        소명 철회
                      </button>
                    )}
                    {t.journalNo && <span className="hint"> 전표 {t.journalNo}</span>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {rows.filter((t) => t.id === open).map((t) => (
        <div key={t.id} className="panel" role="group" aria-label="소명 입력">
          <h2>
            소명 입력 · {t.merchant} {won(t.amount)}
          </h2>
          <div className="field">
            <label htmlFor="ce-purpose">용도(10~200자)</label>
            <textarea id="ce-purpose" maxLength={200} value={form.purpose} onChange={(e) => setForm({ ...form, purpose: e.target.value })} />
          </div>
          <label>
            <input type="checkbox" checked={form.personal} onChange={(e) => setForm({ ...form, personal: e.target.checked })} /> 개인 사용(환입)
          </label>
          {!form.personal && (
            <>
              <div className="field">
                <label htmlFor="ce-account">계정과목</label>
                <select id="ce-account" value={form.accountCode} onChange={(e) => setForm({ ...form, accountCode: e.target.value, deduct: e.target.value === "5250" ? false : form.deduct })}>
                  <option value="">계정과목을 고르세요</option>
                  {(accounts.data ?? []).map((a) => (
                    <option key={a.code} value={a.code}>
                      {a.code} {a.name}
                    </option>
                  ))}
                </select>
              </div>
              <label>
                <input type="checkbox" checked={form.deduct} onChange={(e) => setForm({ ...form, deduct: e.target.checked })} /> 부가세 공제
              </label>
              {!form.deduct && (
                <div className="field">
                  <label htmlFor="ce-reason">불공제 사유</label>
                  <select id="ce-reason" value={form.vatReason} onChange={(e) => setForm({ ...form, vatReason: e.target.value })}>
                    {VAT_REASONS.map(([k, v]) => (
                      <option key={k} value={k}>
                        {v}
                      </option>
                    ))}
                  </select>
                </div>
              )}
              <div className="field">
                <label htmlFor="ce-file">영수증 첨부(PDF·이미지)</label>
                <input id="ce-file" type="file" accept=".pdf,.png,.jpg,.jpeg" onChange={(e) => e.target.files?.[0] && void upload(e.target.files[0])} />
                {files.map((f) => (
                  <span key={f.id} className="hint">
                    {" "}
                    {f.name}
                  </span>
                ))}
              </div>
            </>
          )}
          <div className="form-row">
            <button type="button" disabled={busy} onClick={() => void submit(t)}>
              소명 제출
            </button>
            <button type="button" className="btn-secondary" onClick={() => setOpen(0)}>
              닫기
            </button>
          </div>
        </div>
      ))}
    </section>
  );
}

// ---- 소명 확인(조직장) ----

export function CardsConfirmPage() {
  const [tick, setTick] = useState(0);
  const list = useLoad(() => api.get<CardTxn[]>("/api/finance/cards/confirm"), [tick]);
  const [picked, setPicked] = useState<number[]>([]);
  const [msg, setMsg] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const [ret, setRet] = useState<{ id: number; reason: string } | null>(null);
  const rows = list.data ?? [];
  const toggle = (id: number) => setPicked((p) => (p.includes(id) ? p.filter((x) => x !== id) : p.length >= 50 ? p : [...p, id]));

  async function confirm() {
    setMsg(null);
    try {
      const res = await api.post<{ id: number; ok: boolean; error: string; journalNo: string }[]>("/api/finance/cards/confirm", { ids: picked });
      const failed = res.filter((r) => !r.ok);
      setMsg(failed.length === 0 ? { tone: "ok", text: `${res.length}건을 확인했습니다.` } : { tone: "error", text: `${res.length - failed.length}건을 확인했고 ${failed.length}건은 하지 못했습니다. ${failed.map((f) => f.error).join(" / ")}` });
      setPicked([]);
      setTick((x) => x + 1);
    } catch (e) {
      setMsg({ tone: "error", text: errorText(e) });
    }
  }
  async function giveBack() {
    if (!ret) return;
    try {
      await api.post(`/api/finance/cards/confirm/${ret.id}/return`, { reason: ret.reason });
      setMsg({ tone: "ok", text: "소명을 반려했습니다. 소지자에게 알림이 갑니다." });
      setRet(null);
      setTick((x) => x + 1);
    } catch (e) {
      setRet(null);
      setMsg({ tone: "error", text: errorText(e) });
    }
  }

  return (
    <section>
      <h1>카드 소명 확인</h1>
      <p className="hint">소속 조직 구성원이 제출한 소명입니다. 확인하면 임시 비용 전표가 만들어지고 재무가 검토해 승인 요청합니다.</p>
      {msg && <Notice tone={msg.tone}>{msg.text}</Notice>}
      {list.error && <Notice tone="error">{list.error}</Notice>}
      {list.loading && <p>불러오는 중…</p>}
      {list.data && rows.length === 0 && <EmptyState>확인할 소명이 없습니다.</EmptyState>}
      {rows.length > 0 && (
        <>
          <div className="form-row">
            <button type="button" disabled={picked.length === 0} onClick={() => void confirm()}>
              선택한 소명 확인({picked.length}건)
            </button>
            <span className="hint">한 번에 50건까지 확인할 수 있습니다.</span>
          </div>
          <TableWrap>
            <table className="stack-table">
              <thead>
                <tr>
                  <th className="nowrap">선택</th>
                  <th>소지자</th>
                  <th className="nowrap">사용일</th>
                  <th>가맹점</th>
                  <th className="num nowrap">금액</th>
                  <th>소명</th>
                  <th>처리</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((t) => (
                  <tr key={t.id}>
                    <td data-label="선택" className="nowrap">
                      <input type="checkbox" aria-label={`${t.merchant} 선택`} checked={picked.includes(t.id)} onChange={() => toggle(t.id)} />
                    </td>
                    <td data-label="소지자">{t.holderName}</td>
                    <td data-label="사용일" className="nowrap">{dateTime(t.usedAt)}</td>
                    <td data-label="가맹점">{t.merchant}</td>
                    <td data-label="금액" className="num nowrap">{won(t.amount)}</td>
                    <td data-label="소명">
                      {t.explanation ? (
                        <>
                          {t.explanation.personal ? <Badge tone="warn">개인 사용</Badge> : `${t.explanation.accountCode} ${t.explanation.accountName}`} · {t.explanation.purpose}
                          {!t.explanation.personal && !t.explanation.vatDeduct && <> <Badge tone="muted">불공제({t.explanation.vatReasonLabel})</Badge></>}
                          {t.explanation.attachments.length > 0 && <> · 영수증 {t.explanation.attachments.length}건</>}
                        </>
                      ) : (
                        "—"
                      )}
                    </td>
                    <td data-label="처리">
                      <button type="button" className="btn-secondary" onClick={() => setRet({ id: t.id, reason: "" })}>
                        반려
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        </>
      )}
      {ret && (
        <ConfirmDialog title="소명을 반려할까요?" confirmLabel="반려" onCancel={() => setRet(null)} onConfirm={() => void giveBack()}>
          반려하면 소지자가 보완해 다시 소명합니다.
          <div className="field">
            <label htmlFor="cr-reason">반려 사유(필수)</label>
            <input id="cr-reason" maxLength={200} value={ret.reason} onChange={(e) => setRet({ ...ret, reason: e.target.value })} />
          </div>
        </ConfirmDialog>
      )}
    </section>
  );
}

// ---- 카드 관리·업로드·재무 현황 ----

export function CardsAdminPage() {
  const { can } = useAuth();
  const manage = can("card:manage");
  const importing = can("card:import");
  const [tick, setTick] = useState(0);
  const cards = useLoad(() => api.get<Card[]>("/api/finance/cards"), [tick]);
  const overview = useLoad(() => api.get<CardOverview>("/api/finance/cards/overview"), [tick]);
  const [msg, setMsg] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const [form, setForm] = useState({ alias: "", issuer: "format_a", last4: "", holderQuery: "", holderId: 0 });
  const [people, setPeople] = useState<{ id: number; name: string }[]>([]);
  const [file, setFile] = useState<File | null>(null);
  const [format, setFormat] = useState("");
  const [preview, setPreview] = useState<ImportPreview | null>(null);
  const [refund, setRefund] = useState<{ id: number; reason: string } | null>(null);
  const [holderCard, setHolderCard] = useState<Card | null>(null);
  const [reassign, setReassign] = useState<CardTxn | null>(null);
  const canHolder = can("card:holder_manage");

  async function run(fn: () => Promise<string>) {
    setMsg(null);
    try {
      setMsg({ tone: "ok", text: await fn() });
      setTick((t) => t + 1);
    } catch (e) {
      setMsg({ tone: "error", text: errorText(e) });
    }
  }
  async function findPeople(q: string) {
    setForm((f) => ({ ...f, holderQuery: q, holderId: 0 }));
    if (q.trim().length < 1) return setPeople([]);
    try {
      const r = await api.get<{ people: { id: number; name: string }[] }>(`/api/org/people?q=${encodeURIComponent(q)}&limit=8`);
      setPeople(r.people);
    } catch {
      setPeople([]);
    }
  }
  function fileForm(): FormData {
    const fd = new FormData();
    if (file) fd.append("file", file);
    fd.append("format", format);
    return fd;
  }
  const o = overview.data;

  return (
    <section>
      <h1>법인카드 관리</h1>
      {msg && <Notice tone={msg.tone}>{msg.text}</Notice>}
      {cards.error && <Notice tone="error">{cards.error}</Notice>}
      {cards.data && cards.data.length === 0 && <EmptyState>등록된 카드가 없습니다. 카드를 등록한 뒤 내역을 업로드하세요.</EmptyState>}
      {(cards.data ?? []).length > 0 && (
        <TableWrap>
          <table className="stack-table">
            <thead>
              <tr>
                <th>별칭</th>
                <th>카드사 형식</th>
                <th>소지자</th>
                <th className="nowrap">상태</th>
                <th>관리</th>
              </tr>
            </thead>
            <tbody>
              {(cards.data ?? []).map((c) => (
                <tr key={c.id}>
                  <td data-label="별칭">{c.alias}</td>
                  <td data-label="카드사 형식">{c.issuerLabel}</td>
                  <td data-label="소지자">{c.holderName || "—"}</td>
                  <td data-label="상태" className="nowrap">
                    <Badge tone={c.active ? "ok" : "muted"}>{c.active ? "사용 중" : "중지"}</Badge>
                  </td>
                  <td data-label="관리">
                    {canHolder && (
                      <button type="button" className="btn-secondary" onClick={() => setHolderCard(holderCard?.id === c.id ? null : c)}>
                        소지자 변경
                      </button>
                    )}{" "}
                    {manage && (
                      <button type="button" className="btn-secondary" onClick={() => void run(async () => (await api.post(`/api/finance/cards/${c.id}/active`, { active: !c.active }), c.active ? "카드를 중지했습니다." : "카드를 다시 쓰게 했습니다."))}>
                        {c.active ? "사용 중지" : "사용 재개"}
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {holderCard && (
        <HolderPanel
          title={`${holderCard.alias} 소지자 변경`}
          notice="새 소지자는 이후 업로드되는 내역부터 적용됩니다. 이미 올라온 내역은 변경 전 소지자에게 그대로 남습니다."
          current={holderCard.holderName}
          historyPath={`/api/finance/card-holders/${holderCard.id}`}
          confirmLabel="소지자 변경"
          onClose={() => setHolderCard(null)}
          onPick={(id, name) => run(async () => (await api.post(`/api/finance/cards/${holderCard.id}/holder`, { holderId: id }), setHolderCard(null), `소지자를 ${name} 님으로 바꿨습니다.`))}
        />
      )}
      {manage && (
        <>
          <h2>카드 등록</h2>
          <p className="hint">전체 카드번호는 받지 않습니다. 끝 4자리만 입력하세요.</p>
          <div className="form-row">
            <div className="field">
              <label htmlFor="cd-alias">별칭</label>
              <input id="cd-alias" maxLength={40} value={form.alias} onChange={(e) => setForm({ ...form, alias: e.target.value })} />
            </div>
            <div className="field">
              <label htmlFor="cd-issuer">카드사 형식</label>
              <select id="cd-issuer" value={form.issuer} onChange={(e) => setForm({ ...form, issuer: e.target.value })}>
                <option value="format_a">승인일시 한 칸형</option>
                <option value="format_b">일·시 분리 공급가형</option>
              </select>
            </div>
            <div className="field">
              <label htmlFor="cd-last4">카드번호 끝 4자리</label>
              <input id="cd-last4" inputMode="numeric" maxLength={4} value={form.last4} onChange={(e) => setForm({ ...form, last4: e.target.value })} />
            </div>
            <div className="field">
              <label htmlFor="cd-holder">소지자(이름 검색)</label>
              <input id="cd-holder" value={form.holderQuery} onChange={(e) => void findPeople(e.target.value)} />
              {people.length > 0 && form.holderId === 0 && (
                <div role="listbox" aria-label="소지자 후보">
                  {people.map((p) => (
                    <button key={p.id} type="button" className="btn-secondary" onClick={() => { setForm({ ...form, holderId: p.id, holderQuery: p.name }); setPeople([]); }}>
                      {p.name}
                    </button>
                  ))}
                </div>
              )}
            </div>
            <button
              type="button"
              disabled={!form.alias || form.last4.length !== 4}
              onClick={() => void run(async () => (await api.post("/api/finance/cards", { alias: form.alias, issuer: form.issuer, last4: form.last4, holderId: form.holderId }), setForm({ alias: "", issuer: form.issuer, last4: "", holderQuery: "", holderId: 0 }), "카드를 등록했습니다."))}
            >
              카드 등록
            </button>
          </div>
        </>
      )}
      {importing && (
        <>
          <h2>카드 내역 업로드</h2>
          <p className="hint">카드사에서 내려받은 CSV(UTF-8 또는 엑셀 CP949, 5MB·10,000행까지)를 올리세요. 미리보기로 신규·중복·오류를 확인한 뒤 확정합니다. 같은 행은 한 번만 들어갑니다.</p>
          <div className="form-row">
            <div className="field">
              <label htmlFor="cu-file">CSV 파일</label>
              <input id="cu-file" type="file" accept=".csv,text/csv" onChange={(e) => { setFile(e.target.files?.[0] ?? null); setPreview(null); }} />
            </div>
            <div className="field">
              <label htmlFor="cu-format">카드사 형식</label>
              <select id="cu-format" value={format} onChange={(e) => { setFormat(e.target.value); setPreview(null); }}>
                <option value="">자동으로 알아보기</option>
                <option value="format_a">승인일시 한 칸형</option>
                <option value="format_b">일·시 분리 공급가형</option>
              </select>
            </div>
            <button
              type="button"
              disabled={!file}
              onClick={() =>
                void (async () => {
                  setMsg(null);
                  try {
                    setPreview(await api.post<ImportPreview>("/api/finance/cards/import/preview", fileForm()));
                  } catch (e) {
                    setPreview(null);
                    setMsg({ tone: "error", text: errorText(e) });
                  }
                })()
              }
            >
              미리보기
            </button>
          </div>
          {preview && (
            <div className="panel" aria-label="업로드 미리보기">
              <p>
                {preview.formatLabel} · 전체 {preview.total}행 · 신규 {preview.new}건({won(preview.totalAmount)}) · 중복 {preview.duplicate}건 · 오류 {preview.errors}건
              </p>
              {preview.unregistered.length > 0 && <Notice tone="error">등록이 필요한 카드: {preview.unregistered.join(", ")}. 카드를 등록한 뒤 다시 올리세요(중복은 자동으로 무시됩니다).</Notice>}
              {preview.warnings.map((w) => (
                <Notice key={w} tone="info">
                  {w}
                </Notice>
              ))}
              {preview.errorRows.length > 0 && (
                <ul>
                  {preview.errorRows.slice(0, 10).map((r) => (
                    <li key={r.rowNo}>
                      {r.rowNo}행: {r.reason}
                    </li>
                  ))}
                </ul>
              )}
              <p className="hint">승인번호가 없는 행은 사용 시각·가맹점·금액이 같으면 같은 행으로 봅니다.</p>
              <button
                type="button"
                disabled={preview.new === 0}
                onClick={() =>
                  void run(async () => {
                    const r = await api.post<{ new: number; duplicate: number; errors: number }>("/api/finance/cards/import/confirm", fileForm());
                    setPreview(null);
                    setFile(null);
                    return `신규 ${r.new}건을 저장했고 중복 ${r.duplicate}건은 무시했습니다.${r.errors ? ` 오류 ${r.errors}건은 제외했습니다.` : ""}`;
                  })
                }
              >
                업로드 확정
              </button>
            </div>
          )}
        </>
      )}
      {o && (
        <>
          <h2>재무 현황</h2>
          <p>
            {Object.entries(o.statuses)
              .map(([, v]) => `${v.label} ${v.count}건`)
              .join(" · ") || "카드 내역이 없습니다."}
          </p>
          <OverviewTable title="소명 지연(기한 30일 경과)" rows={o.delayed} empty="소명 지연 건이 없습니다.">
            {(t) =>
              manage && (
                <button type="button" className="btn-secondary" onClick={() => setRefund({ id: t.id, reason: "" })}>
                  환입 요청으로 바꾸기
                </button>
              )
            }
          </OverviewTable>
          <OverviewTable title="소지자 없음(퇴사·미지정)" rows={o.noHolder} empty="소지자가 없는 건이 없습니다.">
            {(t) => (canHolder ? <button type="button" className="btn-secondary" onClick={() => setReassign(t)}>소지자 지정</button> : <span className="hint">재무에게 소지자 지정을 요청하세요</span>)}
          </OverviewTable>
          <OverviewTable title="재무 처리 필요(원거래 없는 취소)" rows={o.needsFinance} empty="재무 처리가 필요한 건이 없습니다." />
          <OverviewTable title="환입 요청" rows={o.refundRequested} empty="환입 요청 건이 없습니다.">
            {(t) =>
              manage && (
                <button type="button" onClick={() => void run(async () => (await api.post(`/api/finance/cards/txns/${t.id}/refund-done`, { refundDate: kstToday(), note: "급여 공제로 정산" }), "환입 완료로 처리했습니다."))}>
                  환입 완료
                </button>
              )
            }
          </OverviewTable>
        </>
      )}
      {reassign && (
        <HolderPanel
          title={`소지자 지정 · ${reassign.merchant} ${won(reassign.amount)}`}
          notice="이 내역의 소명 책임을 새 소지자에게 넘깁니다. 이전 소지자는 더 이상 이 내역을 볼 수 없습니다."
          current={reassign.holderName}
          confirmLabel="소지자 지정"
          onClose={() => setReassign(null)}
          onPick={(id, name) => run(async () => (await api.post(`/api/finance/cards/txns/${reassign.id}/reassign`, { holderId: id }), setReassign(null), `소지자를 ${name} 님으로 지정했습니다.`))}
        />
      )}
      {refund && (
        <ConfirmDialog
          title="환입 요청으로 바꿀까요?"
          confirmLabel="환입 요청"
          onCancel={() => setRefund(null)}
          onConfirm={() => void run(async () => (await api.post(`/api/finance/cards/txns/${refund.id}/refund-request`, { reason: refund.reason }), setRefund(null), "환입 요청으로 바꿨습니다."))}
        >
          개인 사용으로 보고 환입을 요청합니다. 실제 정산은 시스템 밖에서 합니다.
          <div className="field">
            <label htmlFor="cf-reason">사유(필수)</label>
            <input id="cf-reason" maxLength={200} value={refund.reason} onChange={(e) => setRefund({ ...refund, reason: e.target.value })} />
          </div>
        </ConfirmDialog>
      )}
      <p>
        <Link to="/finance/cards/confirm">소명 확인 화면</Link> · <Link to="/finance/cards/mine">내 카드 내역</Link>
      </p>
    </section>
  );
}

function OverviewTable({ title, rows, empty, children }: { title: string; rows: CardTxn[]; empty: string; children?: (t: CardTxn) => React.ReactNode }) {
  return (
    <>
      <h3>{title}</h3>
      {rows.length === 0 && <EmptyState>{empty}</EmptyState>}
      {rows.length > 0 && (
        <TableWrap>
          <table className="stack-table" aria-label={title}>
            <thead>
              <tr>
                <th className="nowrap">사용일</th>
                <th>소지자</th>
                <th>가맹점</th>
                <th className="num nowrap">금액</th>
                <th className="nowrap">상태</th>
                <th>처리</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((t) => (
                <tr key={t.id}>
                  <td data-label="사용일" className="nowrap">{dateTime(t.usedAt)}</td>
                  <td data-label="소지자">{t.holderName || "—"}</td>
                  <td data-label="가맹점">{t.merchant}</td>
                  <td data-label="금액" className="num nowrap">{won(t.amount)}</td>
                  <td data-label="상태" className="nowrap">
                    <CardStatusBadge t={t} /> <DueBadge t={t} />
                  </td>
                  <td data-label="처리">{children?.(t)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
    </>
  );
}

// ---- 소지자 고르기(이름 검색)와 변경 이력 ----

function HolderPanel({ title, notice, current, historyPath, confirmLabel, onClose, onPick }: { title: string; notice: string; current: string; historyPath?: string; confirmLabel: string; onClose: () => void; onPick: (id: number, name: string) => Promise<void> }) {
  const [q, setQ] = useState("");
  const [people, setPeople] = useState<{ id: number; name: string }[]>([]);
  const [picked, setPicked] = useState<{ id: number; name: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const history = useLoad(() => (historyPath ? api.get<{ holderName: string; fromDate: string; changedBy: string; changedAt: string }[]>(historyPath) : Promise.resolve([])), [historyPath]);
  async function search(text: string) {
    setQ(text);
    setPicked(null);
    if (text.trim() === "") return setPeople([]);
    try {
      const r = await api.get<{ people: { id: number; name: string }[] }>(`/api/org/people?q=${encodeURIComponent(text)}&limit=8`);
      setPeople(r.people);
    } catch {
      setPeople([]);
    }
  }
  return (
    <div className="panel" role="group" aria-label={title}>
      <h2>{title}</h2>
      <p>현재 소지자: {current || "없음"}</p>
      <p className="hint">{notice}</p>
      <div className="field">
        <label htmlFor="hp-q">새 소지자(이름 검색)</label>
        <input id="hp-q" value={q} onChange={(e) => void search(e.target.value)} />
        {people.length > 0 && !picked && (
          <div role="listbox" aria-label="소지자 후보">
            {people.map((p) => (
              <button key={p.id} type="button" className="btn-secondary" onClick={() => { setPicked(p); setQ(p.name); setPeople([]); }}>
                {p.name}
              </button>
            ))}
          </div>
        )}
      </div>
      <div className="form-row">
        <button
          type="button"
          disabled={!picked || busy}
          onClick={() => {
            if (!picked) return;
            setBusy(true);
            void onPick(picked.id, picked.name).finally(() => setBusy(false));
          }}
        >
          {confirmLabel}
        </button>
        <button type="button" className="btn-secondary" onClick={onClose}>
          닫기
        </button>
      </div>
      {historyPath && (
        <>
          <h3>소지자 변경 이력</h3>
          {(history.data ?? []).length === 0 ? (
            <EmptyState>변경 이력이 없습니다.</EmptyState>
          ) : (
            <TableWrap>
              <table className="stack-table" aria-label="소지자 변경 이력">
                <thead>
                  <tr>
                    <th>소지자</th>
                    <th className="nowrap">적용 시작일</th>
                    <th>바꾼 사람</th>
                    <th className="nowrap">바꾼 시각</th>
                  </tr>
                </thead>
                <tbody>
                  {(history.data ?? []).map((h, i) => (
                    <tr key={i}>
                      <td data-label="소지자">{h.holderName || "없음"}</td>
                      <td data-label="적용 시작일" className="nowrap">{h.fromDate}</td>
                      <td data-label="바꾼 사람">{h.changedBy || "—"}</td>
                      <td data-label="바꾼 시각" className="nowrap">{dateTime(h.changedAt)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </TableWrap>
          )}
        </>
      )}
    </div>
  );
}
