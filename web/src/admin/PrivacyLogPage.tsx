// 개인정보 조회 기록(B41 명세 6절): 원문을 본 기록과 열람 통계. privacy:log_read(감사). 읽기도 감사에 남는다.
// 값(연락처·계좌 원문, 검색어)은 어디에도 없다. 정보주체는 이름·부서로만 보이고 퇴사자는 "(퇴사)" 로 표시한다.
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api } from "../api/client";
import { ExportButtons } from "../layout/ExportButtons";
import { Badge, EmptyState, Notice, TableWrap } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";

export interface LogEntry {
  id: number;
  at: string;
  actorId: number;
  actorName: string;
  actorUnit: string;
  actorRole: string;
  channel: string;
  category: string;
  screen: string;
  dataset?: string;
  isExport: boolean;
  fields: string[];
  subjectCount: number;
  exposure: string;
  offHours: boolean;
}

interface LogList {
  items: LogEntry[];
  retentionDays: number;
}

export interface SubjectRow {
  type: string;
  id: number;
  name: string;
  unit?: string;
  left: boolean;
}

export const CATEGORY_LABEL: Record<string, string> = { contact: "연락처", account: "계좌", card: "카드 내역", employee_no: "사번", attendance: "근태·휴가 사유" };
export const FIELD_LABEL: Record<string, string> = {
  email: "이메일", phone: "휴대전화", account_no: "계좌번호", business_no: "사업자번호", card_txn: "카드 내역", employee_no: "사번", leave_reason: "휴가 사유", absence_reason: "휴직 사유", proof_file: "증빙 파일",
};
const CHANNEL_LABEL: Record<string, string> = { web: "웹", mcp: "AI 도구", cli: "서버 명령", scim: "계정 동기화", llmgw: "AI 호출", api: "API 키" };

function fmt(s: string): string {
  return new Date(s).toLocaleString("ko-KR");
}

interface VerifyResult {
  ok: boolean;
  chainOk: boolean;
  checked: number;
  brokenId?: number;
  why?: string;
  digestsChecked: number;
  digestOk: boolean;
  digestWhy?: string;
  purged: boolean;
}

interface Stats {
  days: number;
  total: number;
  offHours: number;
  exports: number;
  anomalies: number;
  byCategory: { category: string; requests: number; subjects: number }[];
  byActor: { actorId: number; name: string; unit?: string; requests: number; subjects: number }[];
  large: LogEntry[];
}

export function PrivacyLogPage() {
  const [tab, setTab] = useState<"log" | "stats">("log");
  return (
    <section>
      <h1>개인정보 조회 기록</h1>
      <p>연락처·계좌·카드 내역·사번 원문이 화면·내보내기·AI 도구로 나간 기록입니다. 기록은 고칠 수 없고, 이 화면을 보는 것도 기록됩니다.</p>
      <div role="tablist" aria-label="개인정보 조회 기록" style={{ display: "flex", gap: 8, margin: "12px 0" }}>
        <button type="button" role="tab" className={tab === "log" ? "" : "btn-secondary"} aria-selected={tab === "log"} onClick={() => setTab("log")}>
          조회 기록
        </button>
        <button type="button" role="tab" className={tab === "stats" ? "" : "btn-secondary"} aria-selected={tab === "stats"} onClick={() => setTab("stats")}>
          열람 통계
        </button>
      </div>
      {tab === "log" ? <LogTab /> : <StatsTab />}
    </section>
  );
}

function LogTab() {
  const [category, setCategory] = useState("");
  const [actor, setActor] = useState("");
  const [subject, setSubject] = useState("");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [exportOnly, setExportOnly] = useState(false);
  const [items, setItems] = useState<LogEntry[]>([]);
  const [retention, setRetention] = useState(0);
  const [more, setMore] = useState(false);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [open, setOpen] = useState<number | null>(null);
  const [verify, setVerify] = useState<VerifyResult | null>(null);
  const [verifying, setVerifying] = useState(false);

  const load = useCallback(
    async (before?: number) => {
      setLoading(true);
      setError("");
      try {
        const q = new URLSearchParams({ limit: "100" });
        if (category) q.set("category", category);
        if (actor.trim()) q.set("actor", actor.trim());
        if (subject.trim()) q.set("subject", subject.trim());
        if (from) q.set("from", from);
        // 종료일은 그 날 하루를 포함한다(서버는 한국 시간 하루의 시작으로 읽으므로 다음 날 시작을 보낸다).
        if (to) q.set("to", new Date(new Date(`${to}T00:00:00+09:00`).getTime() + 24 * 3600 * 1000).toISOString());
        if (exportOnly) q.set("export", "1");
        if (before) q.set("before", String(before));
        const res = await api.get<LogList>(`/api/admin/privacy/log?${q.toString()}`);
        setItems((cur) => (before ? [...cur, ...res.items] : res.items));
        setRetention(res.retentionDays);
        setMore(res.items.length === 100);
      } catch (err) {
        setError(errorText(err));
      } finally {
        setLoading(false);
      }
    },
    [category, actor, subject, from, to, exportOnly],
  );

  useEffect(() => {
    void load();
  }, [load]);

  async function runVerify() {
    setVerifying(true);
    setError("");
    try {
      setVerify(await api.post<VerifyResult>("/api/admin/privacy/verify"));
    } catch (err) {
      setError(errorText(err));
    } finally {
      setVerifying(false);
    }
  }

  return (
    <div>
      <form className="form-row" onSubmit={(e: FormEvent) => e.preventDefault()}>
        <label>
          범주{" "}
          <select value={category} onChange={(e) => setCategory(e.target.value)}>
            <option value="">전체</option>
            {Object.entries(CATEGORY_LABEL).map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </select>
        </label>
        <label>
          열람자 이름 <input value={actor} onChange={(e) => setActor(e.target.value)} maxLength={40} />
        </label>
        <label>
          정보주체 이름 <input value={subject} onChange={(e) => setSubject(e.target.value)} maxLength={40} />
        </label>
        <label>
          시작일 <input type="date" value={from} max={to || undefined} onChange={(e) => setFrom(e.target.value)} />
        </label>
        <label>
          종료일 <input type="date" value={to} min={from || undefined} onChange={(e) => setTo(e.target.value)} />
        </label>
        <label>
          <input type="checkbox" checked={exportOnly} onChange={(e) => setExportOnly(e.target.checked)} /> 내보내기만
        </label>
      </form>
      <p>
        <button type="button" className="btn-secondary" disabled={verifying} onClick={() => void runVerify()}>
          무결성 확인
        </button>{" "}
        {retention > 0 && <span className="hint">보관 기간 {retention}일(법정 보관 기간은 회사 규모·정보 종류에 따라 달라 법무 확인이 필요합니다)</span>}
      </p>
      <ExportButtons
        path="/api/admin/privacy/log/export"
        params={{
          category,
          actor: actor.trim(),
          subject: subject.trim(),
          from,
          to: to ? new Date(new Date(`${to}T00:00:00+09:00`).getTime() + 24 * 3600 * 1000).toISOString() : "",
          export: exportOnly,
        }}
        disabled={loading || items.length === 0}
      />
      {verify && (
        <Notice tone={verify.ok ? "ok" : "error"}>
          {verify.ok
            ? `조회 기록 ${verify.checked}건과 감사 체인의 하루 요약 ${verify.digestsChecked}건을 확인했습니다. 어긋난 곳이 없습니다.${verify.purged ? " (보관 기간이 지나 파기한 달은 요약으로만 확인)" : ""}`
            : !verify.chainOk
              ? `조회 기록 ${verify.brokenId}번에서 어긋났습니다: ${verify.why}`
              : `감사 체인의 하루 요약과 맞지 않습니다: ${verify.digestWhy}`}
        </Notice>
      )}
      {error && <Notice tone="error">{error}</Notice>}
      {!loading && !error && items.length === 0 ? (
        <EmptyState hint="원문이 화면·내보내기로 나가면 여기에 쌓입니다.">조회 기록이 없습니다.</EmptyState>
      ) : (
        <TableWrap>
          <table className="stack-table" aria-label="조회 기록">
            <thead>
              <tr>
                <th>시각</th>
                <th>열람자</th>
                <th>범주</th>
                <th>화면</th>
                <th>필드</th>
                <th>정보주체</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {items.map((e) => (
                <RowWithSubjects key={e.id} e={e} open={open === e.id} onToggle={() => setOpen(open === e.id ? null : e.id)} />
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      {more && (
        <button type="button" className="btn-secondary" disabled={loading} onClick={() => void load(items[items.length - 1]?.id)}>
          더 보기
        </button>
      )}
    </div>
  );
}

function RowWithSubjects({ e, open, onToggle }: { e: LogEntry; open: boolean; onToggle: () => void }) {
  return (
    <>
      <tr>
        <td data-label="시각" className="nowrap">
          {fmt(e.at)} {e.offHours && <Badge tone="warn">업무시간 밖</Badge>}
        </td>
        <td data-label="열람자">
          {e.actorName || `사용자 ${e.actorId}`}
          {e.actorUnit && <span className="hint"> {e.actorUnit}</span>}
          {e.channel !== "web" && <span className="hint"> · {CHANNEL_LABEL[e.channel] ?? e.channel}</span>}
        </td>
        <td data-label="범주">
          {CATEGORY_LABEL[e.category] ?? e.category}
          {e.isExport && <Badge tone="info">내보내기</Badge>}
          {e.exposure === "mcp_anomaly" && <Badge tone="bad">AI 도구 원문 유출</Badge>}
        </td>
        <td data-label="화면">{e.screen}</td>
        <td data-label="필드">{e.fields.map((f) => FIELD_LABEL[f] ?? f).join(", ") || "-"}</td>
        <td data-label="정보주체">{e.subjectCount}명</td>
        <td>
          <button type="button" className="btn-secondary btn-sm" aria-expanded={open} onClick={onToggle}>
            {open ? "접기" : "정보주체 보기"}
          </button>
        </td>
      </tr>
      {open && (
        <tr>
          <td colSpan={7}>
            <Subjects id={e.id} total={e.subjectCount} />
          </td>
        </tr>
      )}
    </>
  );
}

function Subjects({ id, total }: { id: number; total: number }) {
  const [limit, setLimit] = useState(200);
  const res = useLoad(() => api.get<{ items: SubjectRow[]; total: number }>(`/api/admin/privacy/log/${id}/subjects?limit=${limit}`), [id, limit]);
  if (res.error) return <Notice tone="error">{res.error}</Notice>;
  if (!res.data) return <p>불러오는 중…</p>;
  const unknown = Math.max(0, total - res.data.total);
  return (
    <div>
      <ul aria-label="정보주체">
        {res.data.items.map((s) => (
          <li key={`${s.type}-${s.id}`}>
            {s.name}
            {s.left && " (퇴사)"}
            {s.unit && <span className="hint"> {s.unit}</span>}
          </li>
        ))}
      </ul>
      {res.data.total > res.data.items.length && (
        <button type="button" className="btn-secondary btn-sm" onClick={() => setLimit(limit + 200)}>
          더 보기({res.data.items.length}/{res.data.total})
        </button>
      )}
      {unknown > 0 && <p className="hint">이름을 식별하지 못한 {unknown}명이 더 있습니다(내보내기 행).</p>}
    </div>
  );
}

function StatsTab() {
  const [days, setDays] = useState(30);
  const res = useLoad(() => api.get<Stats>(`/api/admin/privacy/stats?days=${days}`), [days]);
  if (res.error) return <Notice tone="error">{res.error}</Notice>;
  const d = res.data;
  return (
    <div>
      <p>
        <label>
          기간{" "}
          <select value={days} onChange={(e) => setDays(Number(e.target.value))}>
            <option value={7}>최근 7일</option>
            <option value={30}>최근 30일</option>
            <option value={90}>최근 90일</option>
            <option value={365}>최근 1년</option>
          </select>
        </label>
      </p>
      {!d ? (
        <p>불러오는 중…</p>
      ) : d.total === 0 ? (
        <EmptyState>이 기간의 조회 기록이 없습니다.</EmptyState>
      ) : (
        <>
          <p aria-label="요약">
            전체 {d.total}건 · 내보내기 {d.exports}건 · 업무시간 밖 {d.offHours}건
            {d.anomalies > 0 && <Badge tone="bad">AI 도구 원문 유출 {d.anomalies}건</Badge>}
          </p>
          <section className="card" aria-labelledby="st-cat">
            <h2 id="st-cat">범주별</h2>
            <ul>
              {d.byCategory.map((c) => (
                <li key={c.category}>
                  {CATEGORY_LABEL[c.category] ?? c.category}: {c.requests}건, 정보주체 {c.subjects}명
                </li>
              ))}
            </ul>
          </section>
          <section className="card" aria-labelledby="st-actor">
            <h2 id="st-actor">많이 본 사람(상위 10)</h2>
            <ul>
              {d.byActor.map((a) => (
                <li key={a.actorId}>
                  {a.name || `사용자 ${a.actorId}`}
                  {a.unit && <span className="hint"> {a.unit}</span>}: {a.requests}건, 정보주체 {a.subjects}명
                </li>
              ))}
            </ul>
          </section>
          <section className="card" aria-labelledby="st-large">
            <h2 id="st-large">한 번에 많이 본 요청(상위 10)</h2>
            <ul>
              {d.large.map((e) => (
                <li key={e.id}>
                  {fmt(e.at)} · {e.actorName || `사용자 ${e.actorId}`} · {e.screen} · {e.subjectCount}명
                </li>
              ))}
            </ul>
          </section>
        </>
      )}
    </div>
  );
}
