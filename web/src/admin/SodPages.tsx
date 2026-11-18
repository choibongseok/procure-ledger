// 권한 분리(SoD) 화면(B41 명세 8절): 규칙(모드 변경), 위반 목록, 예외 요청·승인.
// 규칙 코드는 서버 규칙 표가 정본이다. 화면은 서버가 내려 준 제목·단계만 보여 준다.
import { useState, type FormEvent } from "react";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Badge, ConfirmDialog, EmptyState, Notice, TableWrap, type Tone } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";

interface RuleRow {
  code: string;
  kind: "txn" | "hold";
  title: string;
  stepA: string;
  stepB: string;
  mode: "off" | "warn" | "block";
  default: string;
  locked: boolean;
  violations30d: number;
  blocked30d: number;
  holders: number;
}

interface ViolationRow {
  id: number;
  at: string;
  rule: string;
  ruleTitle: string;
  result: "allowed" | "blocked" | "excepted";
  kind: string;
  subjectType: string;
  subjectId: number;
  subjectNo: string;
  actorName: string;
  note: string;
}

interface ExceptionRow {
  id: number;
  rule: string;
  ruleTitle: string;
  subjectType: string;
  subjectId: number;
  subjectNo: string;
  userName: string;
  reason: string;
  requestedName: string;
  requestedAt: string;
  approvedName?: string;
  status: "requested" | "active" | "used" | "rejected" | "expired" | "revoked";
  expiresAt?: string;
}

const MODE_LABEL: Record<string, string> = { off: "끔", warn: "경고", block: "차단" };
const RESULT: Record<string, { text: string; tone: Tone }> = {
  allowed: { text: "경고(통과)", tone: "warn" },
  blocked: { text: "차단", tone: "bad" },
  excepted: { text: "예외 통과", tone: "info" },
};
const EX_STATUS: Record<ExceptionRow["status"], { text: string; tone: Tone }> = {
  requested: { text: "승인 대기", tone: "info" },
  active: { text: "사용 가능", tone: "ok" },
  used: { text: "사용함", tone: "muted" },
  rejected: { text: "반려", tone: "bad" },
  expired: { text: "만료", tone: "muted" },
  revoked: { text: "회수", tone: "muted" },
};
const SUBJECT_LABEL: Record<string, string> = { payment: "지급", journal: "전표", purchase_request: "구매요청", card_txn: "카드 내역", user: "사용자", role_grant: "역할 부여", role_change_request: "역할 변경 요청" };

const fmt = (s: string) => new Date(s).toLocaleString("ko-KR");

export function SodRulesPage() {
  const { can } = useAuth();
  const res = useLoad(() => api.get<RuleRow[]>("/api/admin/sod/rules"), []);
  const [note, setNote] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const [pending, setPending] = useState<{ rule: RuleRow; mode: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const manage = can("sod:manage");

  async function apply(rule: RuleRow, mode: string) {
    setPending(null);
    setBusy(true);
    setNote(null);
    try {
      await api.put(`/api/admin/sod/rules/${rule.code}`, { mode });
      setNote({ tone: "ok", text: `"${rule.title}" 규칙을 ${MODE_LABEL[mode]} 모드로 바꿨습니다.` });
      await res.reload();
    } catch (err) {
      setNote({ tone: "error", text: errorText(err) });
    } finally {
      setBusy(false);
    }
  }

  function change(rule: RuleRow, mode: string) {
    if (mode === rule.mode) return;
    if (mode === "off" || (rule.mode === "block" && mode === "warn")) setPending({ rule, mode });
    else void apply(rule, mode);
  }

  return (
    <section>
      <h1>권한 분리 규칙</h1>
      <p>같은 사람이 서로 견제해야 할 두 단계를 모두 하지 못하게 하는 규칙입니다. 경고는 통과시키되 위반을 기록하고 알리며, 차단은 거래를 거부합니다. 잠긴 규칙은 이미 코드가 막고 있어 바꿀 수 없습니다.</p>
      {note && <Notice tone={note.tone}>{note.text}</Notice>}
      {res.error && <Notice tone="error">{res.error}</Notice>}
      {res.data && (
        <TableWrap>
          <table className="stack-table" aria-label="권한 분리 규칙">
            <thead>
              <tr>
                <th>규칙</th>
                <th>종류</th>
                <th>모드</th>
                <th>최근 30일</th>
              </tr>
            </thead>
            <tbody>
              {res.data.map((r) => (
                <tr key={r.code}>
                  <td data-label="규칙">
                    {r.title}
                    <div className="hint">
                      {r.stepA} + {r.stepB}
                    </div>
                  </td>
                  <td data-label="종류">{r.kind === "txn" ? "같은 거래" : "권한 동시 보유"}</td>
                  <td data-label="모드">
                    {r.locked || !manage ? (
                      <>
                        <Badge tone={r.mode === "block" ? "bad" : r.mode === "warn" ? "warn" : "muted"}>{MODE_LABEL[r.mode]}</Badge>
                        {r.locked && <span className="hint"> 잠김</span>}
                      </>
                    ) : (
                      <select aria-label={`${r.title} 모드`} value={r.mode} disabled={busy} onChange={(e) => change(r, e.target.value)}>
                        <option value="off">끔</option>
                        <option value="warn">경고</option>
                        <option value="block">차단</option>
                      </select>
                    )}
                  </td>
                  <td data-label="최근 30일">
                    {r.kind === "hold" ? `보유자 ${r.holders}명 · ` : ""}위반 {r.violations30d}건{r.blocked30d > 0 ? ` (차단 ${r.blocked30d})` : ""}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {pending && (
        <ConfirmDialog title="규칙 약화" confirmLabel="바꾸기" onConfirm={() => void apply(pending.rule, pending.mode)} onCancel={() => setPending(null)}>
          <p>
            "{pending.rule.title}" 규칙을 {MODE_LABEL[pending.rule.mode]}에서 {MODE_LABEL[pending.mode]}으로 낮춥니다. 이후 같은 사람이 두 단계를 하는 것을 {pending.mode === "off" ? "막지도 기록하지도" : "막지 않고 기록만"} 합니다. 변경은 감사 로그에 남습니다.
          </p>
        </ConfirmDialog>
      )}
    </section>
  );
}

function lastMonth(): string {
  const d = new Date(Date.now() + 9 * 3600 * 1000);
  d.setUTCDate(1);
  d.setUTCMonth(d.getUTCMonth() - 1);
  return d.toISOString().slice(0, 7);
}

/** 월별 점검 보고서(XLSX). 권한 분리 조회와 조회 기록 열람 권한이 모두 있는 사용자(감사)에게만 보인다. 파일은 요청할 때 만든다. */
function ReportDownload() {
  const [month, setMonth] = useState(lastMonth);
  return (
    <div className="field" role="group" aria-label="보고서 내려받기">
      <label htmlFor="report-month">월별 점검 보고서</label>
      <input id="report-month" type="month" value={month} max={new Date(Date.now() + 9 * 3600 * 1000).toISOString().slice(0, 7)} onChange={(e) => setMonth(e.target.value)} />
      <a className="btn-secondary" href={`/api/audit/privacy/report?month=${encodeURIComponent(month)}`} download>
        엑셀로 내려받기
      </a>
      <span className="hint">위반·예외·조회 기록 요약·권한 보유 현황 5개 시트입니다. 정보주체 이름은 들어 있지 않습니다.</span>
    </div>
  );
}

export function SodViolationsPage() {
  const { can } = useAuth();
  const [rule, setRule] = useState("");
  const [result, setResult] = useState("");
  const rules = useLoad(() => api.get<RuleRow[]>("/api/admin/sod/rules"), []);
  const res = useLoad(() => api.get<ViolationRow[]>(`/api/admin/sod/violations?rule=${encodeURIComponent(rule)}&result=${encodeURIComponent(result)}`), [rule, result]);
  return (
    <section>
      <h1>권한 분리 위반</h1>
      <p>경고로 통과했거나 차단했거나 예외로 통과한 기록입니다. 같은 규칙의 위반이 한 시간에 10건을 넘으면 알림은 묶어서 한 번만 보냅니다.</p>
      {can("privacy:log_read") && <ReportDownload />}
      <div className="field">
        <label htmlFor="sv-rule">규칙</label>
        <select id="sv-rule" value={rule} onChange={(e) => setRule(e.target.value)}>
          <option value="">전체</option>
          {rules.data?.map((r) => (
            <option key={r.code} value={r.code}>
              {r.title}
            </option>
          ))}
        </select>
      </div>
      <div className="field">
        <label htmlFor="sv-result">결과</label>
        <select id="sv-result" value={result} onChange={(e) => setResult(e.target.value)}>
          <option value="">전체</option>
          <option value="allowed">경고(통과)</option>
          <option value="blocked">차단</option>
          <option value="excepted">예외 통과</option>
        </select>
      </div>
      {res.error && <Notice tone="error">{res.error}</Notice>}
      {res.data && res.data.length === 0 && <EmptyState hint="규칙에 걸리는 시도가 있으면 여기에 쌓입니다.">위반 기록이 없습니다.</EmptyState>}
      {res.data && res.data.length > 0 && (
        <TableWrap>
          <table className="stack-table" aria-label="권한 분리 위반">
            <thead>
              <tr>
                <th>시각</th>
                <th>규칙</th>
                <th>결과</th>
                <th>행위자</th>
                <th>대상</th>
              </tr>
            </thead>
            <tbody>
              {res.data.map((v) => (
                <tr key={v.id}>
                  <td data-label="시각" className="nowrap">
                    {fmt(v.at)}
                  </td>
                  <td data-label="규칙">{v.ruleTitle}</td>
                  <td data-label="결과">
                    <Badge tone={RESULT[v.result].tone}>{RESULT[v.result].text}</Badge>
                  </td>
                  <td data-label="행위자">{v.actorName || "-"}</td>
                  <td data-label="대상">
                    {SUBJECT_LABEL[v.subjectType] ?? v.subjectType} {v.subjectNo || `#${v.subjectId}`}
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

export function SodExceptionsPage() {
  const { can } = useAuth();
  const rules = useLoad(() => api.get<RuleRow[]>("/api/admin/sod/rules"), []);
  const res = useLoad(() => api.get<ExceptionRow[]>("/api/admin/sod/exceptions"), []);
  const [note, setNote] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [form, setForm] = useState({ rule: "", subjectType: "payment", subjectId: "", subjectNo: "", userId: "", reason: "" });
  const eligible = (rules.data ?? []).filter((r) => r.kind === "txn" && !r.locked);

  async function call(fn: () => Promise<unknown>, ok: string) {
    setBusy(true);
    setNote(null);
    try {
      await fn();
      setNote({ tone: "ok", text: ok });
      await res.reload();
    } catch (err) {
      setNote({ tone: "error", text: errorText(err) });
    } finally {
      setBusy(false);
    }
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    void call(
      () => api.post("/api/admin/sod/exceptions", { rule: form.rule, subjectType: form.subjectType, subjectId: Number(form.subjectId), subjectNo: form.subjectNo, userId: Number(form.userId), reason: form.reason }),
      "예외를 요청했습니다. 요청자와 대상자가 아닌 승인 담당자가 승인해야 합니다.",
    );
  }

  const decide = (id: number, action: "approve" | "reject" | "revoke", ok: string) => call(() => api.post(`/api/admin/sod/exceptions/${id}/${action}`, {}), ok);

  return (
    <section>
      <h1>권한 분리 예외</h1>
      <p>차단 규칙을 특정 거래 한 건에 한해 풀어 주는 예외입니다. 승인 뒤 7일 안에 한 번만 쓸 수 있고, 요청자·승인자·대상자는 서로 달라야 합니다. 잠긴 규칙은 예외를 만들 수 없습니다.</p>
      {note && <Notice tone={note.tone}>{note.text}</Notice>}
      {can("sod:manage") && (
        <form onSubmit={submit} aria-label="예외 요청">
          <div className="field">
            <label htmlFor="ex-rule">규칙</label>
            <select id="ex-rule" value={form.rule} onChange={(e) => setForm({ ...form, rule: e.target.value })} required>
              <option value="">선택</option>
              {eligible.map((r) => (
                <option key={r.code} value={r.code}>
                  {r.title}
                </option>
              ))}
            </select>
          </div>
          <div className="field">
            <label htmlFor="ex-type">거래 종류</label>
            <select id="ex-type" value={form.subjectType} onChange={(e) => setForm({ ...form, subjectType: e.target.value })}>
              <option value="payment">지급</option>
              <option value="journal">전표</option>
              <option value="purchase_request">구매요청</option>
              <option value="card_txn">카드 내역</option>
            </select>
          </div>
          <div className="field">
            <label htmlFor="ex-id">거래 번호(내부 번호)</label>
            <input id="ex-id" type="number" min={1} value={form.subjectId} onChange={(e) => setForm({ ...form, subjectId: e.target.value })} required />
          </div>
          <div className="field">
            <label htmlFor="ex-no">화면에 보일 번호(선택)</label>
            <input id="ex-no" maxLength={40} value={form.subjectNo} onChange={(e) => setForm({ ...form, subjectNo: e.target.value })} />
          </div>
          <div className="field">
            <label htmlFor="ex-user">예외로 처리할 사람(사용자 번호)</label>
            <input id="ex-user" type="number" min={1} value={form.userId} onChange={(e) => setForm({ ...form, userId: e.target.value })} required />
          </div>
          <div className="field">
            <label htmlFor="ex-reason">사유(10자 이상)</label>
            <input id="ex-reason" minLength={10} maxLength={300} value={form.reason} onChange={(e) => setForm({ ...form, reason: e.target.value })} required />
          </div>
          <button type="submit" disabled={busy}>
            예외 요청
          </button>
        </form>
      )}
      {res.error && <Notice tone="error">{res.error}</Notice>}
      {res.data && res.data.length === 0 && <EmptyState>예외 내역이 없습니다.</EmptyState>}
      {res.data && res.data.length > 0 && (
        <TableWrap>
          <table className="stack-table" aria-label="권한 분리 예외">
            <thead>
              <tr>
                <th>규칙</th>
                <th>대상 거래</th>
                <th>예외 대상자</th>
                <th>사유</th>
                <th>요청자</th>
                <th>상태</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {res.data.map((x) => (
                <tr key={x.id}>
                  <td data-label="규칙">{x.ruleTitle}</td>
                  <td data-label="대상 거래">
                    {SUBJECT_LABEL[x.subjectType] ?? x.subjectType} {x.subjectNo || `#${x.subjectId}`}
                  </td>
                  <td data-label="예외 대상자">{x.userName}</td>
                  <td data-label="사유">{x.reason}</td>
                  <td data-label="요청자">
                    {x.requestedName}
                    <div className="hint">{fmt(x.requestedAt)}</div>
                  </td>
                  <td data-label="상태">
                    <Badge tone={EX_STATUS[x.status].tone}>{EX_STATUS[x.status].text}</Badge>
                    {x.status === "active" && x.expiresAt && <div className="hint">{fmt(x.expiresAt)} 까지</div>}
                  </td>
                  <td>
                    {can("sod:exception_approve") && x.status === "requested" && (
                      <>
                        <button type="button" className="btn-sm" disabled={busy} onClick={() => void decide(x.id, "approve", "예외를 승인했습니다. 7일 안에 한 번 쓸 수 있습니다.")}>
                          승인
                        </button>{" "}
                        <button type="button" className="btn-secondary btn-sm" disabled={busy} onClick={() => void decide(x.id, "reject", "예외를 반려했습니다.")}>
                          반려
                        </button>
                      </>
                    )}
                    {can("sod:exception_approve") && x.status === "active" && (
                      <button type="button" className="btn-secondary btn-sm" disabled={busy} onClick={() => void decide(x.id, "revoke", "예외를 회수했습니다.")}>
                        회수
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
  );
}
