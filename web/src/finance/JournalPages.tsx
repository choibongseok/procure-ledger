// 전표 목록·입력·상세(B31 3.2). 승인은 전자결재(전표 승인 요청서)로 한다. 승인된 전표는 고칠 수 없고 역분개로만 정정한다.
import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Badge, ConfirmDialog, EmptyState, Notice, TableWrap } from "../layout/ui";
import { dateTime, won } from "../purchase/labels";
import { businessMessage, errorText, useLoad } from "../purchase/useLoad";
import { KIND_CHOICES, STATUS_CHOICES, STATUS_TONE, type Account, type BatchResult, type Journal } from "./types";
import { hasSiteChoice, useSites } from "./useSites";

const kstToday = () => new Date(Date.now() + 9 * 3600 * 1000).toISOString().slice(0, 10);

export function StatusBadge({ j }: { j: Pick<Journal, "status" | "statusLabel"> }) {
  return <Badge tone={STATUS_TONE[j.status]}>{j.statusLabel}</Badge>;
}

export function JournalListPage() {
  const { can } = useAuth();
  const [draft, setDraft] = useState({ status: "", kind: "", from: "", to: "", account: "" });
  const [applied, setApplied] = useState(draft);
  const [offset, setOffset] = useState(0);
  const qs = new URLSearchParams(Object.entries({ ...applied, offset: offset ? String(offset) : "" }).filter(([, v]) => v) as [string, string][]).toString();
  const list = useLoad(() => api.get<{ rows: Journal[]; total: number }>(`/api/finance/journals${qs ? `?${qs}` : ""}`), [qs]);
  const accounts = useLoad(() => api.get<Account[]>("/api/finance/accounts?all=1"), []);
  const rows = list.data?.rows ?? [];
  const total = list.data?.total ?? 0;
  const [picked, setPicked] = useState<string[]>([]);
  const [batchBusy, setBatchBusy] = useState(false);
  const [batchMsg, setBatchMsg] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const draftNos = rows.filter((j) => j.status === "draft").map((j) => j.no);
  const toggle = (no: string) => setPicked((p) => (p.includes(no) ? p.filter((x) => x !== no) : p.length >= 20 ? p : [...p, no]));
  async function requestBatch() {
    setBatchBusy(true);
    setBatchMsg(null);
    try {
      const res = await api.post<BatchResult[]>("/api/finance/journals/request-batch", { nos: picked });
      const failed = res.filter((r) => !r.ok);
      setBatchMsg(
        failed.length === 0
          ? { tone: "ok", text: `${res.length}건을 승인 요청했습니다.` }
          : { tone: "error", text: `${res.length - failed.length}건을 승인 요청했고 ${failed.length}건은 하지 못했습니다. ${failed.map((f) => `${f.no}: ${businessMessage(f.error)}`).join(" / ")}` },
      );
      setPicked([]);
      list.reload?.();
    } catch (e) {
      setBatchMsg({ tone: "error", text: errorText(e) });
    } finally {
      setBatchBusy(false);
    }
  }
  return (
    <section>
      <h1>전표</h1>
      <div className="form-row">
        <div className="field">
          <label htmlFor="jl-status">상태</label>
          <select id="jl-status" value={draft.status} onChange={(e) => setDraft({ ...draft, status: e.target.value })}>
            {STATUS_CHOICES.map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="jl-kind">종류</label>
          <select id="jl-kind" value={draft.kind} onChange={(e) => setDraft({ ...draft, kind: e.target.value })}>
            {KIND_CHOICES.map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="jl-from">전기일 시작</label>
          <input id="jl-from" type="date" value={draft.from} onChange={(e) => setDraft({ ...draft, from: e.target.value })} />
        </div>
        <div className="field">
          <label htmlFor="jl-to">전기일 종료</label>
          <input id="jl-to" type="date" value={draft.to} onChange={(e) => setDraft({ ...draft, to: e.target.value })} />
        </div>
        <div className="field">
          <label htmlFor="jl-acc">계정</label>
          <select id="jl-acc" value={draft.account} onChange={(e) => setDraft({ ...draft, account: e.target.value })}>
            <option value="">전체</option>
            {(accounts.data ?? []).map((a) => (
              <option key={a.code} value={a.code}>
                {a.code} {a.name}
              </option>
            ))}
          </select>
        </div>
        <button
          type="button"
          onClick={() => {
            setOffset(0);
            setApplied(draft);
          }}
        >
          조회
        </button>
        {can("ledger:write") && (
          <Link className="btn" to="/finance/journals/new">
            새 전표 입력
          </Link>
        )}
      </div>
      {list.error && <Notice tone="error">{list.error}</Notice>}
      {batchMsg && <Notice tone={batchMsg.tone}>{batchMsg.text}</Notice>}
      {list.loading && <p>불러오는 중…</p>}
      {list.data && rows.length === 0 && <EmptyState>조건에 맞는 전표가 없습니다.</EmptyState>}
      {can("ledger:write") && draftNos.length > 0 && (
        <div className="form-row">
          <button type="button" disabled={batchBusy || picked.length === 0} onClick={() => void requestBatch()}>
            선택한 전표 승인 요청({picked.length}건)
          </button>
          <button type="button" className="btn-secondary" onClick={() => setPicked(draftNos.slice(0, 20))}>
            임시 전표 모두 선택(20건까지)
          </button>
          <span className="hint">한 번에 20건까지 승인 요청할 수 있습니다.</span>
        </div>
      )}
      {rows.length > 0 && (
        <>
          <TableWrap>
            <table className="stack-table">
              <thead>
                <tr>
                  {can("ledger:write") && <th className="nowrap">선택</th>}
                  <th className="nowrap">전표 번호</th>
                  <th className="nowrap">종류</th>
                  <th>적요</th>
                  <th className="nowrap">전기일</th>
                  <th className="num nowrap">차변 합계</th>
                  <th className="nowrap">상태</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((j) => (
                  <tr key={j.no}>
                    {can("ledger:write") && (
                      <td data-label="선택" className="nowrap">
                        {j.status === "draft" ? <input type="checkbox" aria-label={`${j.no} 선택`} checked={picked.includes(j.no)} onChange={() => toggle(j.no)} /> : "—"}
                      </td>
                    )}
                    <td data-label="전표 번호" className="nowrap">
                      <Link to={`/finance/journals/${j.no}`}>{j.no}</Link>
                    </td>
                    <td data-label="종류" className="nowrap">{j.kindLabel}</td>
                    <td data-label="적요">
                      {j.memo}
                      {j.intercompany && <> <Badge tone="info">법인 간 거래({j.intercompany})</Badge></>}
                      {j.corrected && <> <Badge tone="warn">정정됨({j.reversedByNo})</Badge></>}
                    </td>
                    <td data-label="전기일" className="nowrap">{j.postingDate}</td>
                    <td data-label="차변 합계" className="num nowrap">{won(j.totalAmount)}</td>
                    <td data-label="상태" className="nowrap">
                      <StatusBadge j={j} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
          <p className="hint">
            {total}건 중 {offset + 1}~{offset + rows.length}
          </p>
          <div className="form-row">
            <button type="button" className="btn-secondary" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - 50))}>
              이전
            </button>
            <button type="button" className="btn-secondary" disabled={offset + 50 >= total} onClick={() => setOffset(offset + 50)}>
              다음
            </button>
          </div>
        </>
      )}
    </section>
  );
}

interface LineDraft {
  accountCode: string;
  side: "D" | "C";
  amount: string;
  vendorId: string;
  memo: string;
}
const emptyLine = (side: "D" | "C"): LineDraft => ({ accountCode: "", side, amount: "", vendorId: "", memo: "" });
const toInt = (s: string) => Number(s.replace(/[^0-9]/g, "")) || 0;

/** 전표 입력·수정 화면(임시 전표만). no 가 있으면 수정이다. */
export function JournalEditPage() {
  const { no } = useParams();
  const nav = useNavigate();
  const accounts = useLoad(() => api.get<Account[]>("/api/finance/accounts"), []);
  const vendors = useLoad(() => api.get<{ id: number; name: string }[]>("/api/vendors"), []);
  const existing = useLoad(() => (no ? api.get<Journal>(`/api/finance/journals/${no}`) : Promise.resolve(null)), [no]);
  const [posting, setPosting] = useState(kstToday());
  const [trade, setTrade] = useState("");
  const [memo, setMemo] = useState("");
  const [kind, setKind] = useState("general");
  const [siteId, setSiteId] = useState(0);
  const siteChoices = useSites();
  const [lines, setLines] = useState<LineDraft[]>([emptyLine("D"), emptyLine("C")]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [loaded, setLoaded] = useState(false);

  const ex = existing.data;
  if (ex && !loaded) {
    setLoaded(true);
    setPosting(ex.postingDate);
    setTrade(ex.tradeDate);
    setMemo(ex.memo);
    setKind(ex.kind);
    setSiteId(ex.siteId ?? 0);
    setLines((ex.lines ?? []).map((l) => ({ accountCode: l.accountCode, side: l.side, amount: String(l.amount), vendorId: l.vendorId ? String(l.vendorId) : "", memo: l.memo })));
  }
  const debit = lines.filter((l) => l.side === "D").reduce((a, l) => a + toInt(l.amount), 0);
  const credit = lines.filter((l) => l.side === "C").reduce((a, l) => a + toInt(l.amount), 0);
  const set = (i: number, p: Partial<LineDraft>) => setLines(lines.map((l, j) => (j === i ? { ...l, ...p } : l)));

  async function save(thenRequest: boolean) {
    setBusy(true);
    setError("");
    try {
      const body = {
        kind: ex && ex.kind !== "general" && ex.kind !== "opening" ? "" : kind,
        postingDate: posting,
        tradeDate: trade || posting,
        memo,
        siteId,
        lines: lines.map((l) => ({ accountCode: l.accountCode, side: l.side, amount: toInt(l.amount), vendorId: Number(l.vendorId) || 0, memo: l.memo })),
      };
      const saved = no ? await api.put<Journal>(`/api/finance/journals/${no}`, body) : await api.post<Journal>("/api/finance/journals", body);
      if (thenRequest) await api.post(`/api/finance/journals/${saved.no}/request`);
      nav(`/finance/journals/${saved.no}`);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  if (existing.error) return <Notice tone="error">{existing.error}</Notice>;
  if (no && ex && ex.status !== "draft") return <Notice tone="warn">임시 상태의 전표만 고칠 수 있습니다.</Notice>;
  return (
    <section>
      <h1>{no ? `전표 수정 ${no}` : "새 전표 입력"}</h1>
      <p className="hint">차변 합계와 대변 합계가 같아야 승인을 요청할 수 있습니다. 임시 저장은 합계가 달라도 됩니다. 외상매입금 줄은 거래처를 골라야 합니다.</p>
      {error && <Notice tone="error">{error}</Notice>}
      <div className="form-row">
        <div className="field">
          <label htmlFor="je-kind">종류</label>
          <select id="je-kind" value={kind} disabled={!!ex && ex.kind !== "general" && ex.kind !== "opening"} onChange={(e) => setKind(e.target.value)}>
            <option value="general">일반</option>
            <option value="opening">기초</option>
          </select>
        </div>
        <div className="field">
          <label htmlFor="je-post">전기일</label>
          <input id="je-post" type="date" value={posting} onChange={(e) => setPosting(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="je-trade">거래일</label>
          <input id="je-trade" type="date" value={trade} onChange={(e) => setTrade(e.target.value)} />
        </div>
        {hasSiteChoice(siteChoices) && (
          <div className="field">
            <label htmlFor="je-site">사업장</label>
            <select id="je-site" value={siteId} onChange={(e) => setSiteId(Number(e.target.value))}>
              <option value={0}>주사업장</option>
              {siteChoices
                .filter((s) => s.kind !== "head")
                .map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.name}
                  </option>
                ))}
            </select>
          </div>
        )}
        <div className="field" style={{ flex: 1 }}>
          <label htmlFor="je-memo">적요(1~200자)</label>
          <input id="je-memo" value={memo} maxLength={200} onChange={(e) => setMemo(e.target.value)} />
        </div>
      </div>
      <TableWrap>
        <table className="stack-table">
          <thead>
            <tr>
              <th className="nowrap">구분</th>
              <th>계정</th>
              <th className="num nowrap">금액(원)</th>
              <th>거래처</th>
              <th>줄 적요</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {lines.map((l, i) => (
              <tr key={i}>
                <td data-label="구분">
                  <select aria-label={`${i + 1}번째 줄 차변·대변`} value={l.side} onChange={(e) => set(i, { side: e.target.value as "D" | "C" })}>
                    <option value="D">차변</option>
                    <option value="C">대변</option>
                  </select>
                </td>
                <td data-label="계정">
                  <select aria-label={`${i + 1}번째 줄 계정`} value={l.accountCode} onChange={(e) => set(i, { accountCode: e.target.value })}>
                    <option value="">선택</option>
                    {(accounts.data ?? []).map((a) => (
                      <option key={a.code} value={a.code}>
                        {a.code} {a.name}
                      </option>
                    ))}
                  </select>
                </td>
                <td data-label="금액(원)" className="num">
                  <input aria-label={`${i + 1}번째 줄 금액`} inputMode="numeric" value={l.amount} onChange={(e) => set(i, { amount: e.target.value.replace(/[^0-9]/g, "") })} />
                </td>
                <td data-label="거래처">
                  <select aria-label={`${i + 1}번째 줄 거래처`} value={l.vendorId} onChange={(e) => set(i, { vendorId: e.target.value })}>
                    <option value="">없음</option>
                    {(vendors.data ?? []).map((v) => (
                      <option key={v.id} value={v.id}>
                        {v.name}
                      </option>
                    ))}
                  </select>
                </td>
                <td data-label="줄 적요">
                  <input aria-label={`${i + 1}번째 줄 적요`} value={l.memo} maxLength={200} onChange={(e) => set(i, { memo: e.target.value })} />
                </td>
                <td>
                  {lines.length > 2 && (
                    <button type="button" className="btn-secondary btn-sm" onClick={() => setLines(lines.filter((_, j) => j !== i))}>
                      줄 삭제
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>
      <div className="form-row">
        <button type="button" className="btn-secondary" onClick={() => setLines([...lines, emptyLine(debit > credit ? "C" : "D")])}>
          줄 추가
        </button>
        <span aria-live="polite">
          차변 {won(debit)} · 대변 {won(credit)} ·{" "}
          {debit === credit ? <Badge tone="ok">차대 일치</Badge> : <Badge tone="warn">차이 {won(Math.abs(debit - credit))}</Badge>}
        </span>
      </div>
      <div className="form-row">
        <button type="button" disabled={busy} onClick={() => void save(false)}>
          임시 저장
        </button>
        <button type="button" className="btn-secondary" disabled={busy || debit !== credit || debit === 0} onClick={() => void save(true)}>
          저장하고 승인 요청
        </button>
        <Link to={no ? `/finance/journals/${no}` : "/finance/journals"}>취소</Link>
      </div>
    </section>
  );
}

/** 전표 상세: 줄·증빙·결재 문서·역분개 연결과 상태별 동작 단추. */
export function JournalDetailPage() {
  const { no } = useParams();
  const { can } = useAuth();
  const nav = useNavigate();
  const j = useLoad(() => api.get<Journal>(`/api/finance/journals/${no}`), [no]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [dialog, setDialog] = useState<"cancel" | "reverse" | null>(null);
  const [reason, setReason] = useState("");
  const d = j.data;
  const write = can("ledger:write");

  async function act(fn: () => Promise<unknown>) {
    setBusy(true);
    setError("");
    try {
      await fn();
      setDialog(null);
      await j.reload();
    } catch (e) {
      setDialog(null);
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  if (j.error) return <Notice tone="error">{j.error}</Notice>;
  if (!d) return <p>불러오는 중…</p>;
  const lines = d.lines ?? [];
  return (
    <section>
      <h1>
        전표 {d.no} <StatusBadge j={d} /> {d.corrected && <Badge tone="warn">정정됨({d.reversedByNo})</Badge>}
      </h1>
      {error && <Notice tone="error">{error}</Notice>}
      <dl className="detail-grid">
        <dt>종류</dt>
        <dd>{d.kindLabel}</dd>
        <dt>전기일</dt>
        <dd>{d.postingDate}</dd>
        <dt>거래일</dt>
        <dd>{d.tradeDate}</dd>
        <dt>적요</dt>
        <dd>{d.memo}</dd>
        {d.siteName && (
          <>
            <dt>사업장</dt>
            <dd>{d.siteName}</dd>
          </>
        )}
        {d.intercompany && (
          <>
            <dt>법인 간 거래</dt>
            <dd>
              <Badge tone="info">법인 간 거래({d.intercompany})</Badge>
            </dd>
          </>
        )}
        <dt>입력자</dt>
        <dd>
          {d.createdBy} · {dateTime(d.createdAt)}
        </dd>
        {d.approvedAt && (
          <>
            <dt>승인</dt>
            <dd>
              {d.approvedBy} · {dateTime(d.approvedAt)}
            </dd>
          </>
        )}
        {d.reversesNo && (
          <>
            <dt>원 전표</dt>
            <dd>
              <Link to={`/finance/journals/${d.reversesNo}`}>{d.reversesNo}</Link>
            </dd>
          </>
        )}
        {d.docId > 0 && (
          <>
            <dt>승인 요청서</dt>
            <dd>
              <Link to={`/docs/${d.docId}`}>{d.docNo || "문서 보기"}</Link>
            </dd>
          </>
        )}
        {d.cancelReason && (
          <>
            <dt>취소 사유</dt>
            <dd>{d.cancelReason}</dd>
          </>
        )}
      </dl>
      <TableWrap>
        <table className="stack-table">
          <thead>
            <tr>
              <th className="nowrap">구분</th>
              <th>계정</th>
              <th className="num nowrap">금액</th>
              <th>거래처</th>
              <th>줄 적요</th>
            </tr>
          </thead>
          <tbody>
            {lines.map((l) => (
              <tr key={l.seq}>
                <td data-label="구분" className="nowrap">{l.side === "D" ? "차변" : "대변"}</td>
                <td data-label="계정">
                  {l.accountCode} {l.accountName}
                </td>
                <td data-label="금액" className="num nowrap">{won(l.amount)}</td>
                <td data-label="거래처">{l.vendorName || "—"}</td>
                <td data-label="줄 적요">{l.memo || "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>
      {(d.evidence ?? []).length > 0 && (
        <p>
          연결된 증빙:{" "}
          {(d.evidence ?? []).map((e, i) => (
            <span key={`${e.kind}-${e.refId}`}>
              {i > 0 && ", "}
              {e.kind === "verdict" ? <Link to="/documents">증빙 판정 {e.refId}</Link> : `${e.kind === "document" ? "증빙 문서" : "첨부"} ${e.refId}`}
            </span>
          ))}
        </p>
      )}
      <p>
        차변 합계 {won(lines.filter((l) => l.side === "D").reduce((a, l) => a + l.amount, 0))} · 대변 합계 {won(lines.filter((l) => l.side === "C").reduce((a, l) => a + l.amount, 0))}
      </p>
      {write && (
        <div className="form-row">
          {d.status === "draft" && (
            <>
              <Link className="btn-secondary" to={`/finance/journals/${d.no}/edit`}>
                수정
              </Link>
              <button type="button" disabled={busy} onClick={() => void act(() => api.post(`/api/finance/journals/${d.no}/request`))}>
                승인 요청
              </button>
            </>
          )}
          {(d.status === "draft" || d.status === "requested") && (
            <button type="button" className="btn-danger" disabled={busy} onClick={() => setDialog("cancel")}>
              전표 취소
            </button>
          )}
          {d.status === "approved" && d.kind !== "reversal" && !d.corrected && (
            <button type="button" className="btn-secondary" disabled={busy} onClick={() => setDialog("reverse")}>
              역분개
            </button>
          )}
        </div>
      )}
      {dialog === "cancel" && (
        <ConfirmDialog title="전표를 취소할까요?" confirmLabel="전표 취소" busy={busy} onCancel={() => setDialog(null)} onConfirm={() => void act(() => api.post(`/api/finance/journals/${d.no}/cancel`, { reason }))}>
          취소한 전표는 되살릴 수 없고 번호만 남습니다. 승인 요청 중이면 결재 문서를 회수합니다.
          <div className="field">
            <label htmlFor="jc-reason">취소 사유(필수)</label>
            <input id="jc-reason" value={reason} maxLength={200} onChange={(e) => setReason(e.target.value)} />
          </div>
        </ConfirmDialog>
      )}
      {dialog === "reverse" && (
        <ConfirmDialog
          title="역분개 전표를 만들까요?"
          confirmLabel="역분개 전표 만들기"
          busy={busy}
          onCancel={() => setDialog(null)}
          onConfirm={() =>
            void act(async () => {
              const created = await api.post<Journal>(`/api/finance/journals/${d.no}/reverse`, {});
              nav(`/finance/journals/${created.no}`);
            })
          }
        >
          차대를 반대로 한 임시 전표가 만들어지고, 같은 승인 절차를 거쳐 승인되면 이 전표에 정정됨 표시가 붙습니다. 한 전표는 한 번만 역분개할 수 있습니다.
        </ConfirmDialog>
      )}
    </section>
  );
}
