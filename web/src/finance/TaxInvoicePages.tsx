// 전자세금계산서(B43 1단계) 목록·작성·상세·매입 수기 등록. 매출은 재무가 직접 작성해 발행하고(판매 모듈 전), 국세청 수신이 되기 전에는 접수된 증빙이 아니다.
import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Badge, ConfirmDialog, EmptyState, Notice, TableWrap } from "../layout/ui";
import { dateTime, won } from "../purchase/labels";
import { errorText, useLoad } from "../purchase/useLoad";
import { AMEND_REASON_CHOICES, TAX_STATUS_CHOICES, TAX_STATUS_TONE, TAX_TYPE_CHOICES, type TaxInvoice, type TaxParty } from "./taxTypes";

const kstToday = () => new Date(Date.now() + 9 * 3600 * 1000).toISOString().slice(0, 10);

/** 사업자번호 10자리를 000-00-00000 으로 보인다(가려진 값은 그대로). */
export function bizFmt(v: string): string {
  return /^\d{10}$/.test(v) ? `${v.slice(0, 3)}-${v.slice(3, 5)}-${v.slice(5)}` : v;
}

export function TaxStatusBadge({ v }: { v: Pick<TaxInvoice, "status" | "statusName"> }) {
  return <Badge tone={TAX_STATUS_TONE[v.status]}>{v.statusName}</Badge>;
}

function counterparty(v: TaxInvoice): string {
  return v.direction === "sale" ? v.buyer.name : v.supplier.name;
}

export function TaxInvoiceListPage() {
  const { can } = useAuth();
  const [direction, setDirection] = useState("");
  const [status, setStatus] = useState("");
  const [counter, setCounter] = useState("");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const qs = new URLSearchParams();
  if (direction) qs.set("direction", direction);
  if (status) qs.set("status", status);
  if (counter.trim()) qs.set("counterparty", counter.trim());
  if (from) qs.set("from", from);
  if (to) qs.set("to", to);
  const list = useLoad(() => api.get<{ rows: TaxInvoice[]; total: number }>(`/api/finance/tax-invoices?${qs.toString()}`), [direction, status, counter, from, to]);
  const rows = list.data?.rows ?? [];
  return (
    <section>
      <h1>세금계산서</h1>
      <div className="form-row">
        <div className="field">
          <label htmlFor="ti-dir">구분</label>
          <select id="ti-dir" value={direction} onChange={(e) => setDirection(e.target.value)}>
            <option value="">전체</option>
            <option value="sale">매출(발행)</option>
            <option value="purchase">매입(수신)</option>
          </select>
        </div>
        <div className="field">
          <label htmlFor="ti-status">상태</label>
          <select id="ti-status" value={status} onChange={(e) => setStatus(e.target.value)}>
            {TAX_STATUS_CHOICES.map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="ti-counter">상대방 이름</label>
          <input id="ti-counter" value={counter} onChange={(e) => setCounter(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="ti-from">작성일 시작</label>
          <input id="ti-from" type="date" value={from} onChange={(e) => setFrom(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="ti-to">작성일 끝</label>
          <input id="ti-to" type="date" value={to} onChange={(e) => setTo(e.target.value)} />
        </div>
        {can("taxinvoice:write") && (
          <>
            <Link className="btn" to="/finance/tax-invoices/new">
              매출 세금계산서 작성
            </Link>
            <Link className="btn" to="/finance/tax-invoices/receive">
              매입 수기 등록
            </Link>
          </>
        )}
      </div>
      {list.error && <Notice tone="error">{list.error}</Notice>}
      {list.loading && <p>불러오는 중…</p>}
      {list.data && rows.length === 0 && <EmptyState>조건에 맞는 세금계산서가 없습니다.</EmptyState>}
      {rows.length > 0 && (
        <TableWrap>
          <table className="stack-table">
            <thead>
              <tr>
                <th className="nowrap">번호</th>
                <th className="nowrap">구분</th>
                <th className="nowrap">작성일</th>
                <th>상대방</th>
                <th className="num nowrap">공급가액</th>
                <th className="num nowrap">세액</th>
                <th className="num nowrap">합계</th>
                <th className="nowrap">상태</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((v) => (
                <tr key={v.id}>
                  <td data-label="번호" className="nowrap">
                    <Link to={`/finance/tax-invoices/${v.id}`}>{v.no}</Link>
                  </td>
                  <td data-label="구분" className="nowrap">{v.directionName}</td>
                  <td data-label="작성일" className="nowrap">{v.issueDate}</td>
                  <td data-label="상대방">
                    {counterparty(v)}
                    {v.intercompany && <> <Badge tone="info">법인 간 거래({v.intercompany})</Badge></>}
                  </td>
                  <td data-label="공급가액" className="num nowrap">{won(v.supplyTotal)}</td>
                  <td data-label="세액" className="num nowrap">{won(v.taxTotal)}</td>
                  <td data-label="합계" className="num nowrap">{won(v.total)}</td>
                  <td data-label="상태" className="nowrap">
                    <TaxStatusBadge v={v} />
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

// ---- 작성(매출) ----

interface LineForm {
  name: string;
  qty: string;
  unitPrice: string;
  supply: string;
}

const emptyLine = (): LineForm => ({ name: "", qty: "", unitPrice: "", supply: "" });
const num = (s: string) => (s.trim() === "" ? 0 : Number(s.replace(/,/g, "")));

function PartyFields({ idp, title, value, onChange, emailHint }: { idp: string; title: string; value: TaxParty; onChange: (p: TaxParty) => void; emailHint?: string }) {
  const set = (k: keyof TaxParty, v: string) => onChange({ ...value, [k]: v });
  return (
    <fieldset>
      <legend>{title}</legend>
      <div className="form-row">
        <div className="field">
          <label htmlFor={`${idp}-biz`}>사업자번호</label>
          <input id={`${idp}-biz`} inputMode="numeric" placeholder="000-00-00000" value={value.bizNo} onChange={(e) => set("bizNo", e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor={`${idp}-name`}>상호</label>
          <input id={`${idp}-name`} maxLength={100} value={value.name} onChange={(e) => set("name", e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor={`${idp}-rep`}>대표자</label>
          <input id={`${idp}-rep`} maxLength={50} value={value.rep} onChange={(e) => set("rep", e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor={`${idp}-addr`}>주소</label>
          <input id={`${idp}-addr`} maxLength={200} value={value.addr} onChange={(e) => set("addr", e.target.value)} />
        </div>
        {emailHint !== undefined && (
          <div className="field">
            <label htmlFor={`${idp}-email`}>이메일</label>
            <input id={`${idp}-email`} type="email" maxLength={80} placeholder={emailHint} value={value.email} onChange={(e) => set("email", e.target.value)} />
          </div>
        )}
      </div>
    </fieldset>
  );
}

const blankParty = (): TaxParty => ({ bizNo: "", branchNo: "", name: "", rep: "", addr: "", email: "" });

export function TaxInvoiceEditPage() {
  const { id } = useParams();
  const nav = useNavigate();
  const editing = id !== undefined && id !== "new";
  const loaded = useLoad(() => (editing ? api.get<TaxInvoice>(`/api/finance/tax-invoices/${id}`) : Promise.resolve(null)), [id]);
  const [taxType, setTaxType] = useState("taxable");
  const [issueDate, setIssueDate] = useState(kstToday());
  const [buyer, setBuyer] = useState<TaxParty>(blankParty());
  const [chargeType, setChargeType] = useState("claim");
  const [remark, setRemark] = useState("");
  const [lines, setLines] = useState<LineForm[]>([emptyLine()]);
  const [seeded, setSeeded] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  if (editing && loaded.data && !seeded) {
    const d = loaded.data;
    setSeeded(true);
    setTaxType(d.taxType);
    setIssueDate(d.issueDate);
    setBuyer({ ...d.buyer, bizNo: bizFmt(d.buyer.bizNo), email: "" }); // 가려진 이메일은 되돌려 쓰지 않는다(비우면 변경 없음)
    setChargeType(d.chargeType);
    setRemark(d.remark);
    setLines((d.lines ?? []).map((l) => ({ name: l.name, qty: l.qty ? String(l.qty) : "", unitPrice: l.unitPrice ? String(l.unitPrice) : "", supply: String(l.supply) })));
  }

  const setLine = (i: number, k: keyof LineForm, v: string) =>
    setLines((ls) =>
      ls.map((l, j) => {
        if (j !== i) return l;
        const next = { ...l, [k]: v };
        if ((k === "qty" || k === "unitPrice") && num(next.qty) && num(next.unitPrice)) next.supply = String(num(next.qty) * num(next.unitPrice)); // 수량×단가가 있으면 공급가액을 채운다
        return next;
      }),
    );
  const supplyTotal = lines.reduce((a, l) => a + num(l.supply), 0);
  const draftData = () => ({
    taxType,
    issueDate,
    buyer,
    chargeType,
    remark,
    lines: lines.map((l) => ({ day: "", name: l.name, spec: "", qty: num(l.qty), unitPrice: num(l.unitPrice), supply: num(l.supply), remark: "" })),
  });

  async function save() {
    setBusy(true);
    setError("");
    try {
      const saved = editing ? await api.put<TaxInvoice>(`/api/finance/tax-invoices/${id}`, draftData()) : await api.post<TaxInvoice>("/api/finance/tax-invoices", draftData());
      nav(`/finance/tax-invoices/${saved.id}`);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section>
      <h1>{editing ? "세금계산서 고치기" : "매출 세금계산서 작성"}</h1>
      <Notice tone="info">공급자는 회사 정보로 채워집니다. 세액은 작성일의 부가세율로 서버가 계산합니다. 발행하기 전까지는 고칠 수 있고, 발행한 뒤에는 수정세금계산서로만 정정할 수 있습니다.</Notice>
      {(error || loaded.error) && <Notice tone="error">{error || loaded.error}</Notice>}
      <div className="form-row">
        <div className="field">
          <label htmlFor="te-date">작성일</label>
          <input id="te-date" type="date" max={kstToday()} value={issueDate} onChange={(e) => setIssueDate(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="te-type">과세 유형</label>
          <select id="te-type" value={taxType} onChange={(e) => setTaxType(e.target.value)}>
            {TAX_TYPE_CHOICES.map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="te-charge">영수·청구</label>
          <select id="te-charge" value={chargeType} onChange={(e) => setChargeType(e.target.value)}>
            <option value="claim">청구</option>
            <option value="receipt">영수</option>
          </select>
        </div>
      </div>
      <PartyFields idp="te-buyer" title="공급받는자" value={buyer} onChange={setBuyer} emailHint={editing ? "비워 두면 기존 이메일을 유지합니다" : ""} />
      <h2>품목</h2>
      <TableWrap>
        <table className="stack-table">
          <thead>
            <tr>
              <th>품목명</th>
              <th className="num">수량</th>
              <th className="num">단가</th>
              <th className="num">공급가액</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {lines.map((l, i) => (
              <tr key={i}>
                <td data-label="품목명">
                  <input aria-label={`${i + 1}번째 품목명`} maxLength={100} value={l.name} onChange={(e) => setLine(i, "name", e.target.value)} />
                </td>
                <td data-label="수량" className="num">
                  <input aria-label={`${i + 1}번째 수량`} inputMode="numeric" value={l.qty} onChange={(e) => setLine(i, "qty", e.target.value)} />
                </td>
                <td data-label="단가" className="num">
                  <input aria-label={`${i + 1}번째 단가`} inputMode="numeric" value={l.unitPrice} onChange={(e) => setLine(i, "unitPrice", e.target.value)} />
                </td>
                <td data-label="공급가액" className="num">
                  <input aria-label={`${i + 1}번째 공급가액`} inputMode="numeric" value={l.supply} onChange={(e) => setLine(i, "supply", e.target.value)} />
                </td>
                <td>
                  {lines.length > 1 && (
                    <button type="button" className="btn" onClick={() => setLines((ls) => ls.filter((_, j) => j !== i))}>
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
        <button type="button" className="btn" disabled={lines.length >= 16} onClick={() => setLines((ls) => [...ls, emptyLine()])}>
          줄 추가
        </button>
        <span>공급가액 합계 {won(supplyTotal)}</span>
      </div>
      <div className="field">
        <label htmlFor="te-remark">비고</label>
        <input id="te-remark" maxLength={200} value={remark} onChange={(e) => setRemark(e.target.value)} />
      </div>
      <div className="form-row">
        <button type="button" className="btn btn-primary" disabled={busy} onClick={() => void save()}>
          저장
        </button>
        <Link className="btn" to="/finance/tax-invoices">
          목록
        </Link>
      </div>
    </section>
  );
}

// ---- 상세 ----

type Dialog = null | "issue" | "cancel" | "amend" | "resend" | "cancel-received";

export function TaxInvoiceDetailPage() {
  const { id } = useParams();
  const { can } = useAuth();
  const nav = useNavigate();
  const [rev, setRev] = useState(0);
  const d = useLoad(() => api.get<TaxInvoice>(`/api/finance/tax-invoices/${id}`), [id, rev]);
  const [dialog, setDialog] = useState<Dialog>(null);
  const [reason, setReason] = useState("");
  const [amend, setAmend] = useState("return");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [contact, setContact] = useState<string | null>(null);
  const v = d.data;

  async function act(fn: () => Promise<unknown>) {
    setBusy(true);
    setError("");
    try {
      await fn();
      setDialog(null);
      setReason("");
      setContact(null);
      setRev((n) => n + 1);
    } catch (e) {
      setError(errorText(e));
      setDialog(null);
    } finally {
      setBusy(false);
    }
  }

  async function reveal() {
    try {
      const c = await api.get<{ name: string; email: string }>(`/api/finance/tax-invoices/${id}/contact`);
      setContact(c.email);
    } catch (e) {
      setError(errorText(e));
    }
  }

  if (d.error) return <Notice tone="error">{d.error}</Notice>;
  if (!v) return <p>불러오는 중…</p>;
  const sale = v.direction === "sale";
  const post = (p: string, body?: unknown) => api.post<TaxInvoice>(`/api/finance/tax-invoices/${v.id}/${p}`, body);
  return (
    <section>
      <h1>
        세금계산서 {v.no} <TaxStatusBadge v={v} />
      </h1>
      {error && <Notice tone="error">{error}</Notice>}
      {(v.warnings ?? []).map((w) => (
        <Notice key={w} tone="warn">
          {w}
        </Notice>
      ))}
      {sale && v.status !== "accepted" && v.status !== "canceled" && <Notice tone="warn">이 화면은 국세청에 접수된 증빙이 아닙니다. 국세청 수신 상태가 되면 승인번호가 표시됩니다.</Notice>}
      {v.status === "failed" && <Notice tone="error">전송에 실패했습니다. {v.lastCodeText}</Notice>}
      {v.amendOfNo && (
        <p>
          수정 사유: {v.amendReasonName} (원 세금계산서 <Link to={`/finance/tax-invoices/${v.amendOfId}`}>{v.amendOfNo}</Link>)
        </p>
      )}
      {v.amendedByNo && <p>수정세금계산서가 발행되었습니다: {v.amendedByNo}</p>}
      <dl className="kv">
        <dt>구분</dt>
        <dd>{v.directionName} · {v.taxTypeName}</dd>
        <dt>작성일</dt>
        <dd>{v.issueDate}</dd>
        <dt>승인번호</dt>
        <dd>{v.approvalNo || "아직 없음"}</dd>
        {v.intercompany && (
          <>
            <dt>법인 간 거래</dt>
            <dd>
              <Badge tone="info">법인 간 거래({v.intercompany})</Badge> 상대방이 그룹의 다른 법인입니다. 같은 거래의 상대 법인 건과는 대조 후보로만 제안됩니다.
            </dd>
          </>
        )}
        <dt>공급자</dt>
        <dd>{v.supplier.name} ({bizFmt(v.supplier.bizNo)}) 대표 {v.supplier.rep}{v.supplier.email ? ` · ${v.supplier.email}` : ""}</dd>
        <dt>공급받는자</dt>
        <dd>{v.buyer.name} ({bizFmt(v.buyer.bizNo)}) 대표 {v.buyer.rep}{v.buyer.email ? ` · ${v.buyer.email}` : ""}</dd>
        <dt>공급가액 · 세액 · 합계</dt>
        <dd>{won(v.supplyTotal)} · {won(v.taxTotal)} · {won(v.total)}</dd>
        {v.source === "asp" && (
          <>
            <dt>기록 방식</dt>
            <dd>대행사 수신 동기화로 가져왔습니다. 작성자 칸에는 동기화를 실행한 사람(자동 실행은 대행사 연동 담당자)이 대신 기록되어 있습니다.</dd>
          </>
        )}
        {!sale && v.matchStatus && (
          <>
            <dt>입고 대조</dt>
            <dd>
              {v.matchStatus} · {v.matchResult} <Link to="/finance/tax-matches">세금계산서 대조 보기</Link>
            </dd>
          </>
        )}
        {can("mask:bypass") && (v.direction === "sale" ? v.buyer.email : v.supplier.email) && (
          <>
            <dt>상대방 이메일 원문</dt>
            <dd>
              {contact === null ? (
                <button type="button" className="btn btn-sm" onClick={() => void reveal()}>
                  원문 보기(열람이 기록됩니다)
                </button>
              ) : (
                contact || "등록된 이메일이 없습니다."
              )}
            </dd>
          </>
        )}
        {v.remark && (
          <>
            <dt>비고</dt>
            <dd>{v.remark}</dd>
          </>
        )}
      </dl>
      {(v.lines ?? []).length > 0 && (
        <TableWrap>
          <table className="stack-table">
            <caption>품목</caption>
            <thead>
              <tr>
                <th>품목명</th>
                <th className="num">수량</th>
                <th className="num">단가</th>
                <th className="num">공급가액</th>
                <th className="num">세액</th>
              </tr>
            </thead>
            <tbody>
              {(v.lines ?? []).map((l) => (
                <tr key={l.seq}>
                  <td data-label="품목명">{l.name}</td>
                  <td data-label="수량" className="num">{l.qty || ""}</td>
                  <td data-label="단가" className="num">{l.unitPrice ? won(l.unitPrice) : ""}</td>
                  <td data-label="공급가액" className="num">{won(l.supply)}</td>
                  <td data-label="세액" className="num">{won(l.tax ?? 0)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      <div className="form-row">
        {sale && v.status === "draft" && can("taxinvoice:write") && (
          <Link className="btn" to={`/finance/tax-invoices/${v.id}/edit`}>
            고치기
          </Link>
        )}
        {sale && v.status === "draft" && can("taxinvoice:issue") && (
          <button type="button" className="btn btn-primary" onClick={() => setDialog("issue")}>
            발행
          </button>
        )}
        {sale && v.status === "failed" && can("taxinvoice:issue") && (
          <button type="button" className="btn" onClick={() => setDialog("resend")}>
            다시 전송
          </button>
        )}
        {sale && (v.status === "draft" || v.status === "failed") && can("taxinvoice:write") && (
          <button type="button" className="btn" onClick={() => setDialog("cancel")}>
            {v.status === "failed" ? "포기(취소)" : "취소"}
          </button>
        )}
        {sale && v.status === "accepted" && can("taxinvoice:write") && (
          <button type="button" className="btn" onClick={() => setDialog("amend")}>
            수정 발행
          </button>
        )}
        {!sale && v.status === "accepted" && v.source === "manual" && can("taxinvoice:write") && (
          <button type="button" className="btn" onClick={() => setDialog("cancel-received")}>
            등록 취소
          </button>
        )}
        <Link className="btn" to="/finance/tax-invoices">
          목록
        </Link>
      </div>
      {(v.transmissions ?? []).length > 0 && (
        <TableWrap>
          <table className="stack-table">
            <caption>전송 기록</caption>
            <thead>
              <tr>
                <th className="nowrap">시각</th>
                <th>결과</th>
                <th className="num">시도</th>
              </tr>
            </thead>
            <tbody>
              {(v.transmissions ?? []).map((t, i) => (
                <tr key={i}>
                  <td data-label="시각" className="nowrap">{dateTime(t.at)}</td>
                  <td data-label="결과">{t.kind === "status" ? "수신 조회: " : "발행 요청: "}{t.result}</td>
                  <td data-label="시도" className="num">{t.attemptNo}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {dialog === "issue" && (
        <ConfirmDialog title="세금계산서를 발행할까요?" confirmLabel="발행" variant="primary" busy={busy} onCancel={() => setDialog(null)} onConfirm={() => void act(() => post("issue"))}>
          발행한 뒤에는 금액·작성일·거래 상대방을 고칠 수 없고, 정정은 수정세금계산서로만 할 수 있습니다. 공급가액 {won(v.supplyTotal)}, 세액 {won(v.taxTotal)}이(가) 대행사를 거쳐 국세청에 전송됩니다.
        </ConfirmDialog>
      )}
      {dialog === "resend" && (
        <ConfirmDialog title="다시 전송할까요?" confirmLabel="다시 전송" variant="primary" busy={busy} onCancel={() => setDialog(null)} onConfirm={() => void act(() => post("resend"))}>
          실패한 전송을 다시 시도합니다. 한 건은 5번까지 다시 전송할 수 있습니다.
        </ConfirmDialog>
      )}
      {(dialog === "cancel" || dialog === "cancel-received") && (
        <ConfirmDialog
          title="취소할까요?"
          confirmLabel="취소"
          busy={busy}
          onCancel={() => setDialog(null)}
          onConfirm={() => void act(() => post(dialog === "cancel" ? "cancel" : "cancel-received", { reason }))}
        >
          취소 사유를 적어 주세요.
          <div className="field">
            <label htmlFor="tc-reason">취소 사유(필수)</label>
            <input id="tc-reason" maxLength={200} value={reason} onChange={(e) => setReason(e.target.value)} />
          </div>
        </ConfirmDialog>
      )}
      {dialog === "amend" && (
        <ConfirmDialog
          title="수정세금계산서를 만들까요?"
          confirmLabel="초안 만들기"
          variant="primary"
          busy={busy}
          onCancel={() => setDialog(null)}
          onConfirm={() =>
            void act(async () => {
              const r = await post("amend", { reason: amend });
              nav(`/finance/tax-invoices/${r.id}`);
            })
          }
        >
          원 세금계산서는 그대로 두고, 정정 내용을 담은 새 세금계산서 초안을 만듭니다. 환입·계약 해제·이중 발급은 음수 금액으로 만들어집니다.
          <div className="field">
            <label htmlFor="ta-reason">수정 사유</label>
            <select id="ta-reason" value={amend} onChange={(e) => setAmend(e.target.value)}>
              {AMEND_REASON_CHOICES.map(([k, n]) => (
                <option key={k} value={k}>
                  {n}
                </option>
              ))}
            </select>
          </div>
        </ConfirmDialog>
      )}
    </section>
  );
}

// ---- 매입 수기 등록 ----

export function TaxReceivePage() {
  const nav = useNavigate();
  const [approvalNo, setApprovalNo] = useState("");
  const [taxType, setTaxType] = useState("taxable");
  const [issueDate, setIssueDate] = useState(kstToday());
  const [supplier, setSupplier] = useState<TaxParty>(blankParty());
  const [supply, setSupply] = useState("");
  const [tax, setTax] = useState("");
  const [remark, setRemark] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function save() {
    setBusy(true);
    setError("");
    try {
      const r = await api.post<TaxInvoice>("/api/finance/tax-invoices/receive", { approvalNo, taxType, issueDate, supplier, supplyTotal: num(supply), taxTotal: num(tax), remark });
      nav(`/finance/tax-invoices/${r.id}`);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section>
      <h1>매입 세금계산서 수기 등록</h1>
      <Notice tone="info">대행사 없이 받은 전자세금계산서를 등록합니다. 같은 승인번호는 한 번만 등록할 수 있습니다.</Notice>
      {error && <Notice tone="error">{error}</Notice>}
      <div className="form-row">
        <div className="field">
          <label htmlFor="tr-approval">승인번호(24자리)</label>
          <input id="tr-approval" maxLength={24} value={approvalNo} onChange={(e) => setApprovalNo(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="tr-date">작성일</label>
          <input id="tr-date" type="date" max={kstToday()} value={issueDate} onChange={(e) => setIssueDate(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="tr-type">과세 유형</label>
          <select id="tr-type" value={taxType} onChange={(e) => setTaxType(e.target.value)}>
            {TAX_TYPE_CHOICES.map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </select>
        </div>
      </div>
      <PartyFields idp="tr-supplier" title="공급자" value={supplier} onChange={setSupplier} />
      <div className="form-row">
        <div className="field">
          <label htmlFor="tr-supply">공급가액</label>
          <input id="tr-supply" inputMode="numeric" value={supply} onChange={(e) => setSupply(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="tr-tax">세액</label>
          <input id="tr-tax" inputMode="numeric" value={tax} onChange={(e) => setTax(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="tr-remark">비고</label>
          <input id="tr-remark" maxLength={200} value={remark} onChange={(e) => setRemark(e.target.value)} />
        </div>
      </div>
      <div className="form-row">
        <button type="button" className="btn btn-primary" disabled={busy} onClick={() => void save()}>
          등록
        </button>
        <Link className="btn" to="/finance/tax-invoices">
          목록
        </Link>
      </div>
    </section>
  );
}
