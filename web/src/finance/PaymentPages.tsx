// 지급 요청 목록·작성·상세와 지급 처리(B32 3.1). 승인은 전자결재(지급 요청서)로 한다. 지급 처리는 요청 작성자가 아닌 재무 담당자가 한다.
import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Badge, ConfirmDialog, EmptyState, Notice, TableWrap } from "../layout/ui";
import { dateTime, won } from "../purchase/labels";
import { errorText, useLoad } from "../purchase/useLoad";
import { PAYMENT_STATUS_CHOICES, PAYMENT_STATUS_TONE, type BankAccount, type Candidate, type Payment } from "./payTypes";

const kstToday = () => new Date(Date.now() + 9 * 3600 * 1000).toISOString().slice(0, 10);

export function PaymentStatusBadge({ p }: { p: Pick<Payment, "status" | "statusLabel"> }) {
  return <Badge tone={PAYMENT_STATUS_TONE[p.status]}>{p.statusLabel}</Badge>;
}

export function PaymentListPage() {
  const { can } = useAuth();
  const [status, setStatus] = useState("");
  const list = useLoad(() => api.get<{ rows: Payment[]; total: number }>(`/api/finance/payments${status ? `?status=${status}` : ""}`), [status]);
  const rows = list.data?.rows ?? [];
  return (
    <section>
      <h1>지급 요청</h1>
      <div className="form-row">
        <div className="field">
          <label htmlFor="pl-status">상태</label>
          <select id="pl-status" value={status} onChange={(e) => setStatus(e.target.value)}>
            {PAYMENT_STATUS_CHOICES.map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </select>
        </div>
        {can("payment:write") && (
          <Link className="btn" to="/finance/payments/new">
            새 지급 요청
          </Link>
        )}
      </div>
      {list.error && <Notice tone="error">{list.error}</Notice>}
      {list.loading && <p>불러오는 중…</p>}
      {list.data && rows.length === 0 && <EmptyState>조건에 맞는 지급 요청이 없습니다.</EmptyState>}
      {rows.length > 0 && (
        <TableWrap>
          <table className="stack-table">
            <thead>
              <tr>
                <th className="nowrap">요청 번호</th>
                <th>거래처</th>
                <th className="num nowrap">지급 합계</th>
                <th className="nowrap">지급 예정일</th>
                <th>출금 계좌</th>
                <th className="nowrap">상태</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((p) => (
                <tr key={p.no}>
                  <td data-label="요청 번호" className="nowrap">
                    <Link to={`/finance/payments/${p.no}`}>{p.no}</Link>
                  </td>
                  <td data-label="거래처">{p.vendorName}</td>
                  <td data-label="지급 합계" className="num nowrap">{won(p.total)}</td>
                  <td data-label="지급 예정일" className="nowrap">{p.dueDate}</td>
                  <td data-label="출금 계좌">{p.bankAlias}</td>
                  <td data-label="상태" className="nowrap">
                    <PaymentStatusBadge p={p} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
    </section>
  );
}

export function PaymentEditPage() {
  const nav = useNavigate();
  const vendors = useLoad(() => api.get<{ id: number; name: string }[]>("/api/vendors"), []);
  const banks = useLoad(() => api.get<BankAccount[]>("/api/finance/bank-accounts"), []);
  const [vendorId, setVendorId] = useState(0);
  const cands = useLoad(() => (vendorId ? api.get<Candidate[]>(`/api/finance/payables/${vendorId}/candidates`) : Promise.resolve([] as Candidate[])), [vendorId]);
  const [amounts, setAmounts] = useState<Record<number, string>>({});
  const [bankId, setBankId] = useState(0);
  const [dueDate, setDueDate] = useState("");
  const [memo, setMemo] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const items = Object.entries(amounts)
    .filter(([, v]) => Number(v) > 0)
    .map(([k, v]) => ({ payableId: Number(k), amount: Number(v) }));
  const total = items.reduce((a, i) => a + i.amount, 0);

  async function save(request: boolean) {
    setBusy(true);
    setError("");
    try {
      const created = await api.post<Payment>("/api/finance/payments", { vendorId, dueDate, bankAccountId: bankId || banks.data?.[0]?.id || 0, memo, items });
      if (request) await api.post(`/api/finance/payments/${created.no}/request`);
      nav(`/finance/payments/${created.no}`);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section>
      <h1>새 지급 요청</h1>
      {error && <Notice tone="error">{error}</Notice>}
      <div className="form-row">
        <div className="field">
          <label htmlFor="pe-vendor">거래처</label>
          <select
            id="pe-vendor"
            value={vendorId}
            onChange={(e) => {
              setVendorId(Number(e.target.value));
              setAmounts({});
            }}
          >
            <option value={0}>거래처를 고르세요</option>
            {(vendors.data ?? []).map((v) => (
              <option key={v.id} value={v.id}>
                {v.name}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="pe-bank">출금 회사 계좌</label>
          <select id="pe-bank" value={bankId} onChange={(e) => setBankId(Number(e.target.value))}>
            {(banks.data ?? []).map((b) => (
              <option key={b.id} value={b.id}>
                {b.alias} ({b.accountNo})
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="pe-due">지급 예정일</label>
          <input id="pe-due" type="date" min={kstToday()} value={dueDate} onChange={(e) => setDueDate(e.target.value)} />
        </div>
      </div>
      {vendorId > 0 && cands.data && cands.data.length === 0 && <EmptyState>지급할 수 있는 채무 항목이 없습니다.</EmptyState>}
      {(cands.data ?? []).length > 0 && (
        <TableWrap>
          <table className="stack-table">
            <thead>
              <tr>
                <th className="nowrap">전표 번호</th>
                <th className="nowrap">만기일</th>
                <th className="num nowrap">금액</th>
                <th className="num nowrap">지급 완료</th>
                <th className="num nowrap">지급 요청 중</th>
                <th className="num nowrap">지급 가능액</th>
                <th className="num nowrap">이번 지급액</th>
              </tr>
            </thead>
            <tbody>
              {(cands.data ?? []).map((c) => (
                <tr key={c.payableId}>
                  <td data-label="전표 번호" className="nowrap">
                    <Link to={`/finance/journals/${c.journalNo}`}>{c.journalNo}</Link>
                  </td>
                  <td data-label="만기일" className="nowrap">
                    {c.dueDate}
                    {c.overdueDays > 0 && <> <Badge tone="bad">{c.overdueDays}일 경과</Badge></>}
                  </td>
                  <td data-label="금액" className="num nowrap">{won(c.amount)}</td>
                  <td data-label="지급 완료" className="num nowrap">{won(c.paid)}</td>
                  <td data-label="지급 요청 중" className="num nowrap">
                    {won(c.reserved)}
                    {c.openRequests > 0 ? ` (${c.openRequests}건)` : ""}
                  </td>
                  <td data-label="지급 가능액" className="num nowrap">{won(c.available)}</td>
                  <td data-label="이번 지급액" className="num">
                    <input
                      type="number"
                      min={0}
                      max={c.available}
                      aria-label={`${c.journalNo} 지급액`}
                      value={amounts[c.payableId] ?? ""}
                      onChange={(e) => setAmounts({ ...amounts, [c.payableId]: e.target.value })}
                    />{" "}
                    <button type="button" className="btn-secondary" onClick={() => setAmounts({ ...amounts, [c.payableId]: String(c.available) })}>
                      전액
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      <div className="field">
        <label htmlFor="pe-memo">메모(선택)</label>
        <input id="pe-memo" maxLength={200} value={memo} onChange={(e) => setMemo(e.target.value)} />
      </div>
      <p>지급 합계 {won(total)}</p>
      <div className="form-row">
        <button type="button" className="btn-secondary" disabled={busy || items.length === 0 || !vendorId} onClick={() => void save(false)}>
          임시 저장
        </button>
        <button type="button" disabled={busy || items.length === 0 || !vendorId} onClick={() => void save(true)}>
          저장하고 승인 요청
        </button>
      </div>
    </section>
  );
}

export function PaymentDetailPage() {
  const { no } = useParams();
  const { can, user } = useAuth();
  const nav = useNavigate();
  const [tick, setTick] = useState(0);
  const pay = useLoad(() => api.get<Payment>(`/api/finance/payments/${no}`), [no, tick]);
  const banks = useLoad(() => api.get<BankAccount[]>("/api/finance/bank-accounts"), []);
  const [dialog, setDialog] = useState<"cancel" | "settle" | "reverse" | null>(null);
  const [reason, setReason] = useState("");
  const [paidDate, setPaidDate] = useState(kstToday());
  const [bankId, setBankId] = useState(0);
  const [changeReason, setChangeReason] = useState("");
  const [ref, setRef] = useState("");
  const [confirmAccount, setConfirmAccount] = useState(false);
  const [raw, setRaw] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const d = pay.data;
  const write = can("payment:write");

  async function act(fn: () => Promise<unknown>) {
    setBusy(true);
    setError("");
    try {
      await fn();
      setDialog(null);
      setReason("");
      setRaw("");
      setTick((t) => t + 1);
    } catch (e) {
      setDialog(null);
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  async function reveal() {
    setError("");
    try {
      const r = await api.post<{ accountNo: string }>(`/api/finance/payments/${no}/reveal-payee`);
      setRaw(r.accountNo);
      window.setTimeout(() => setRaw(""), 30000); // 창이 열려 있는 동안만 보이고 곧 다시 가려진다
    } catch (e) {
      setError(errorText(e));
    }
  }

  if (pay.error) return <Notice tone="error">{pay.error}</Notice>;
  if (!d) return <p>불러오는 중…</p>;
  const isCreator = user?.id === d.createdById;
  return (
    <section>
      <h1>
        지급 요청 {d.no} <PaymentStatusBadge p={d} />
      </h1>
      {error && <Notice tone="error">{error}</Notice>}
      {d.warnings.map((w) => (
        <Notice key={w} tone="info">
          {w}
        </Notice>
      ))}
      {d.accountDiffers && <Notice tone="error">거래처 계좌가 변경되었습니다. 승인된 계좌와 다릅니다. 지급 처리 때 확인이 필요합니다.</Notice>}
      <dl className="detail-grid">
        <dt>거래처</dt>
        <dd>{d.vendorName}</dd>
        <dt>지급 합계</dt>
        <dd>{won(d.total)}</dd>
        <dt>지급 예정일</dt>
        <dd>{d.dueDate}</dd>
        <dt>출금 계좌</dt>
        <dd>{d.bankAlias}</dd>
        <dt>받는 계좌</dt>
        <dd>
          {raw || d.payeeAccount}{" "}
          {can("mask:bypass") && !raw && (
            <button type="button" className="btn-secondary" onClick={() => void reveal()}>
              원문 보기
            </button>
          )}
        </dd>
        <dt>작성</dt>
        <dd>
          {d.createdBy} · {dateTime(d.createdAt)}
        </dd>
        {d.docId > 0 && (
          <>
            <dt>지급 요청서</dt>
            <dd>
              <Link to={`/docs/${d.docId}`}>{d.docNo || "문서 보기"}</Link>
            </dd>
          </>
        )}
        {d.memo && (
          <>
            <dt>메모</dt>
            <dd>{d.memo}</dd>
          </>
        )}
        {d.status === "paid" || d.status === "paid_canceled" ? (
          <>
            <dt>지급</dt>
            <dd>
              {d.paidDate} · {d.paidBy}
              {d.transferRef ? ` · 이체 참조번호 ${d.transferRef}` : ""}
            </dd>
            <dt>지급 전표</dt>
            <dd>
              <Link to={`/finance/journals/${d.journalNo}`}>{d.journalNo}</Link>
              {d.reversalNo && (
                <>
                  {" "}
                  · 지급 취소 전표 <Link to={`/finance/journals/${d.reversalNo}`}>{d.reversalNo}</Link>
                </>
              )}
            </dd>
          </>
        ) : null}
        {d.cancelReason && (
          <>
            <dt>취소 사유</dt>
            <dd>{d.cancelReason}</dd>
          </>
        )}
        {d.reverseReason && (
          <>
            <dt>지급 취소 사유</dt>
            <dd>{d.reverseReason}</dd>
          </>
        )}
      </dl>
      <TableWrap>
        <table className="stack-table">
          <thead>
            <tr>
              <th className="nowrap">전표 번호</th>
              <th className="nowrap">발생일</th>
              <th className="nowrap">만기일</th>
              <th className="num nowrap">지급액</th>
            </tr>
          </thead>
          <tbody>
            {(d.items ?? []).map((it) => (
              <tr key={it.payableId}>
                <td data-label="전표 번호" className="nowrap">
                  <Link to={`/finance/journals/${it.journalNo}`}>{it.journalNo}</Link>
                </td>
                <td data-label="발생일" className="nowrap">{it.tradeDate}</td>
                <td data-label="만기일" className="nowrap">{it.dueDate}</td>
                <td data-label="지급액" className="num nowrap">{won(it.amount)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>
      {write && (
        <div className="form-row">
          {d.status === "draft" && (
            <button type="button" disabled={busy} onClick={() => void act(() => api.post(`/api/finance/payments/${d.no}/request`))}>
              승인 요청
            </button>
          )}
          {(d.status === "draft" || d.status === "requested" || d.status === "approved") && (
            <button type="button" className="btn-danger" disabled={busy} onClick={() => setDialog("cancel")}>
              지급 요청 취소
            </button>
          )}
          {d.status === "approved" && !isCreator && (
            <button type="button" disabled={busy} onClick={() => setDialog("settle")}>
              지급 처리
            </button>
          )}
          {d.status === "approved" && isCreator && <span className="hint">지급 요청을 작성한 사람은 지급 처리할 수 없습니다. 다른 재무 담당자가 처리해야 합니다.</span>}
          {d.status === "paid" && !d.reversalNo && (
            <button type="button" className="btn-secondary" disabled={busy} onClick={() => setDialog("reverse")}>
              지급 취소
            </button>
          )}
        </div>
      )}
      {dialog === "cancel" && (
        <ConfirmDialog title="지급 요청을 취소할까요?" confirmLabel="지급 요청 취소" busy={busy} onCancel={() => setDialog(null)} onConfirm={() => void act(() => api.post(`/api/finance/payments/${d.no}/cancel`, { reason }))}>
          취소하면 잡아 둔 지급 가능액(예약)이 풀립니다. 승인 요청 중이면 결재 문서를 회수합니다.
          <div className="field">
            <label htmlFor="pc-reason">취소 사유(필수)</label>
            <input id="pc-reason" maxLength={200} value={reason} onChange={(e) => setReason(e.target.value)} />
          </div>
        </ConfirmDialog>
      )}
      {dialog === "settle" && (
        <ConfirmDialog
          title="지급 처리할까요?"
          confirmLabel="지급 처리"
          busy={busy}
          onCancel={() => setDialog(null)}
          onConfirm={() => void act(() => api.post(`/api/finance/payments/${d.no}/settle`, { paidDate, bankAccountId: bankId || d.bankAccountId, changeReason, transferRef: ref, confirmAccount }))}
        >
          은행에서 실제로 이체한 뒤 기록하세요. 금액 {won(d.total)}은 승인된 그대로이고 바꿀 수 없습니다. 처리하면 지급 전표가 만들어지고 되돌리려면 지급 취소(역분개)를 해야 합니다.
          <div className="field">
            <label htmlFor="ps-date">지급일</label>
            <input id="ps-date" type="date" max={kstToday()} value={paidDate} onChange={(e) => setPaidDate(e.target.value)} />
          </div>
          <div className="field">
            <label htmlFor="ps-bank">출금 회사 계좌</label>
            <select id="ps-bank" value={bankId || d.bankAccountId} onChange={(e) => setBankId(Number(e.target.value))}>
              {(banks.data ?? []).map((b) => (
                <option key={b.id} value={b.id}>
                  {b.alias}
                </option>
              ))}
            </select>
          </div>
          {bankId !== 0 && bankId !== d.bankAccountId && (
            <div className="field">
              <label htmlFor="ps-why">계좌를 바꾸는 이유</label>
              <input id="ps-why" value={changeReason} onChange={(e) => setChangeReason(e.target.value)} />
            </div>
          )}
          <div className="field">
            <label htmlFor="ps-ref">이체 참조번호(선택)</label>
            <input id="ps-ref" maxLength={40} value={ref} onChange={(e) => setRef(e.target.value)} />
          </div>
          {d.accountDiffers && (
            <label>
              <input type="checkbox" checked={confirmAccount} onChange={(e) => setConfirmAccount(e.target.checked)} /> 거래처 계좌가 바뀐 것을 확인했습니다
            </label>
          )}
        </ConfirmDialog>
      )}
      {dialog === "reverse" && (
        <ConfirmDialog
          title="지급을 취소할까요?"
          confirmLabel="지급 취소"
          busy={busy}
          onCancel={() => setDialog(null)}
          onConfirm={() =>
            void act(async () => {
              const r = await api.post<Payment>(`/api/finance/payments/${d.no}/reverse`, { reason });
              if (r.warnings.length > 0) setError(r.warnings.join(" "));
              nav(`/finance/payments/${d.no}`);
            })
          }
        >
          지급 전표의 역분개 전표를 만들어 승인 요청합니다. 승인되면 채무 지급 누계가 되돌아가고 이 지급 건은 지급 취소가 됩니다.
          <div className="field">
            <label htmlFor="pr-reason">지급 취소 사유(필수)</label>
            <input id="pr-reason" maxLength={200} value={reason} onChange={(e) => setReason(e.target.value)} />
          </div>
        </ConfirmDialog>
      )}
    </section>
  );
}
