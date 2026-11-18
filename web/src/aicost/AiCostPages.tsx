//(B49 구현은 한 사람이 맡는다). 부서별 AI 비용 화면: 전사 집계(cost:read)와 우리 부서 화면(report:read).
import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { ApiError, api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Badge, EmptyState, Notice, TableWrap } from "../layout/ui";
import { businessMessage } from "../purchase/useLoad";

export interface CostRow {
  entityId: number;
  departmentId: number;
  departmentName: string;
  purpose: string;
  provider: string;
  model: string;
  calls: number;
  failed: number;
  inputTokens: number;
  outputTokens: number;
  costMicroUsd: number;
  costKrw: number;
}
export interface DeptTotal {
  entityId: number;
  departmentId: number;
  departmentName: string;
  calls: number;
  costMicroUsd: number;
  costKrw: number;
}
export interface ProviderTotal {
  provider: string;
  calls: number;
  costMicroUsd: number;
  costKrw: number;
  actualKrw?: number;
  adjustRatio?: number;
  zeroPriceCalls: number;
}
export interface Adjustment {
  id: number;
  month: string;
  provider: string;
  actualKrw: number;
  note: string;
  enteredByName: string;
  enteredAt: string;
}
export interface CostReport {
  month: string;
  fxUsdKrwX1000: number;
  totalMicroUsd: number;
  totalKrw: number;
  calls: number;
  failed: number;
  departments: DeptTotal[];
  providers: ProviderTotal[];
  rows: CostRow[];
  adjustments: Adjustment[];
  warnings: string[];
}
export interface TrendPoint {
  month: string;
  calls: number;
  costMicroUsd: number;
  costKrw: number;
}
export interface MyReport {
  month: string;
  departmentId: number;
  departmentName: string;
  fxUsdKrwX1000: number;
  totalMicroUsd: number;
  totalKrw: number;
  calls: number;
  forecast?: { elapsedDays: number; daysInMonth: number; costMicroUsd: number; costKrw: number };
  rows: CostRow[];
  trend: TrendPoint[];
  topUsers: { name: string; calls: number; costMicroUsd: number; costKrw: number }[];
  budget?: { amountKrw: number; usedKrw: number; pct: number; fxEstimated: boolean };
  warnings: string[];
}

/** 용도 코드를 화면용 한국어로 바꾼다. 모르는 값은 코드 대신 일반 문구로 보인다. */
export const PURPOSE_LABELS: Record<string, string> = {
  "docverify.extract": "증빙 문서 판정",
  "docverify.quote": "견적서 판정",
  "mcp.select_tool": "AI 도구 선택",
  "knowledge.answer": "사내 지식 질문",
  "eval.run": "평가 세트 실행",
  "policy.search": "규정 검색",
};
export function purposeLabel(p: string): string {
  return PURPOSE_LABELS[p] ?? "기타 용도";
}

/** USD 마이크로 단위를 "$1.23" 으로 보인다. */
export function usd(micro: number): string {
  return `$${(micro / 1_000_000).toLocaleString("ko-KR", { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
}
export function won(n: number): string {
  return `${n.toLocaleString("ko-KR")}원`;
}
/** 환율이 있으면 원화, 없으면 달러로 금액을 보인다. */
export function money(micro: number, krw: number, fx: number): string {
  return fx > 0 ? won(krw) : usd(micro);
}
export function fxText(x1000: number): string {
  return (x1000 / 1000).toLocaleString("ko-KR", { minimumFractionDigits: 1, maximumFractionDigits: 3 });
}

function errorText(e: unknown): string {
  if (e instanceof ApiError) {
    try {
      const j = JSON.parse(e.message) as { error?: string };
      if (j.error) return businessMessage(j.error);
    } catch {
      /* 그대로 */
    }
    return businessMessage(e.message);
  }
  return "요청을 처리하지 못했습니다.";
}

function thisMonth(now: Date = new Date()): string {
  return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, "0")}`;
}

function Warnings({ items }: { items: string[] }) {
  return (
    <>
      {items.map((w) => (
        <Notice key={w} tone="warn">
          {w}
        </Notice>
      ))}
    </>
  );
}

export function AiCostAdminPage() {
  const { can } = useAuth();
  const allocate = can("cost:allocate");
  const [month, setMonth] = useState(thisMonth());
  const [dept, setDept] = useState("");
  const [purpose, setPurpose] = useState("");
  const [provider, setProvider] = useState("");
  const [data, setData] = useState<CostReport | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [fxInput, setFxInput] = useState("");
  const [adjProvider, setAdjProvider] = useState("");
  const [adjAmount, setAdjAmount] = useState("");
  const [adjNote, setAdjNote] = useState("");
  const [msg, setMsg] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    const q = new URLSearchParams({ month });
    if (dept) q.set("department", dept);
    if (purpose) q.set("purpose", purpose);
    if (provider) q.set("provider", provider);
    try {
      setData(await api.get<CostReport>(`/api/admin/ai-cost?${q.toString()}`));
    } catch (e) {
      setError(errorText(e));
    } finally {
      setLoading(false);
    }
  }, [month, dept, purpose, provider]);

  useEffect(() => {
    void load();
  }, [load]);

  const deptOptions = useMemo(() => (data ? data.departments : []), [data]);

  async function saveFx(e: FormEvent) {
    e.preventDefault();
    setMsg("");
    const n = Number(fxInput.replace(/,/g, ""));
    if (!Number.isFinite(n) || n <= 0) {
      setError("환율은 숫자로 입력하세요.");
      return;
    }
    try {
      await api.put("/api/admin/ai-cost/fx", { month, usdKrwX1000: Math.round(n * 1000) });
      setMsg("환율을 저장했습니다.");
      setFxInput("");
      await load();
    } catch (err) {
      setError(errorText(err));
    }
  }

  async function saveAdj(e: FormEvent) {
    e.preventDefault();
    setMsg("");
    const n = Number(adjAmount.replace(/,/g, ""));
    if (!adjProvider || !Number.isInteger(n) || n < 0) {
      setError("공급자를 고르고 청구액을 0 이상의 정수(원)로 입력하세요.");
      return;
    }
    try {
      await api.post("/api/admin/ai-cost/adjustments", { month, provider: adjProvider, actualKrw: n, note: adjNote });
      setMsg("청구액을 저장했습니다.");
      setAdjAmount("");
      setAdjNote("");
      await load();
    } catch (err) {
      setError(errorText(err));
    }
  }

  const fx = data?.fxUsdKrwX1000 ?? 0;
  const empty = data !== null && data.calls === 0 && data.failed === 0;

  return (
    <div>
      <h1>AI 비용</h1>
      <p>AI 호출 기록의 비용을 월·부서·용도·공급자별로 모아 보여 줍니다. 금액은 호출 당시 공급자 단가로 기록된 값이며, 실제 청구액과 다르면 아래에서 청구액을 입력해 비교합니다.</p>
      <p>
        <Link to="/admin/ai-cost/rules">배부 규칙</Link> · <Link to="/admin/ai-cost/preview">배부 미리보기·확정</Link> · <Link to="/admin/ai-cost/budgets">예산</Link>
      </p>
      <form
        aria-label="조회 조건"
        className="form-row"
        onSubmit={(e) => {
          e.preventDefault();
          void load();
        }}
      >
        <label>
          월 <input type="month" value={month} onChange={(e) => setMonth(e.target.value)} required />
        </label>
        <label>
          부서{" "}
          <select value={dept} onChange={(e) => setDept(e.target.value)}>
            <option value="">전체</option>
            {deptOptions.map((d) => (
              <option key={`${d.entityId}-${d.departmentId}`} value={d.departmentId}>
                {d.departmentName}
              </option>
            ))}
          </select>
        </label>
        <label>
          용도{" "}
          <select value={purpose} onChange={(e) => setPurpose(e.target.value)}>
            <option value="">전체</option>
            {Object.entries(PURPOSE_LABELS).map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </select>
        </label>
        <label>
          공급자{" "}
          <select value={provider} onChange={(e) => setProvider(e.target.value)}>
            <option value="">전체</option>
            {(data?.providers ?? []).map((p) => (
              <option key={p.provider} value={p.provider}>
                {p.provider}
              </option>
            ))}
          </select>
        </label>
      </form>
      {error && <Notice tone="error">{error}</Notice>}
      {msg && <Notice tone="ok">{msg}</Notice>}
      {loading && !data && <p>불러오는 중…</p>}
      {data && (
        <>
          <Warnings items={data.warnings} />
          {empty ? (
            <EmptyState>이 달에는 AI 사용 기록이 없습니다.</EmptyState>
          ) : (
            <>
              <p>
                {data.month} · 합계 <strong>{money(data.totalMicroUsd, data.totalKrw, fx)}</strong>
                {fx > 0 && <> ({usd(data.totalMicroUsd)} × 환율 {fxText(fx)})</>} · 호출 {data.calls.toLocaleString("ko-KR")}건 · 실패·차단 {data.failed.toLocaleString("ko-KR")}건(금액에 넣지 않음)
              </p>
              <h2>부서별</h2>
              <TableWrap>
                <table aria-label="부서별 AI 비용">
                  <thead>
                    <tr>
                      <th>부서</th>
                      <th className="num nowrap">호출</th>
                      <th className="num nowrap">금액</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.departments.map((d) => (
                      <tr key={`${d.entityId}-${d.departmentId}`}>
                        <td>{d.departmentName}</td>
                        <td className="num">{d.calls.toLocaleString("ko-KR")}</td>
                        <td className="num">{money(d.costMicroUsd, d.costKrw, fx)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </TableWrap>
              <h2>공급자별과 실제 청구액</h2>
              <TableWrap>
                <table aria-label="공급자별 AI 비용">
                  <thead>
                    <tr>
                      <th>공급자</th>
                      <th className="num nowrap">호출</th>
                      <th className="num nowrap">집계 금액</th>
                      <th className="num nowrap">실제 청구액</th>
                      <th className="num nowrap">청구액 ÷ 집계</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.providers.map((p) => (
                      <tr key={p.provider}>
                        <td>
                          {p.provider}
                          {p.zeroPriceCalls > 0 && (
                            <>
                              {" "}
                              <Badge tone="warn">단가 0</Badge>
                            </>
                          )}
                        </td>
                        <td className="num">{p.calls.toLocaleString("ko-KR")}</td>
                        <td className="num">{money(p.costMicroUsd, p.costKrw, fx)}</td>
                        <td className="num">{p.actualKrw === undefined ? "-" : won(p.actualKrw)}</td>
                        <td className="num">{p.adjustRatio ? `${p.adjustRatio.toFixed(2)}배` : "-"}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </TableWrap>
              <h2>용도·모델별 상세</h2>
              <TableWrap>
                <table aria-label="AI 비용 상세">
                  <thead>
                    <tr>
                      <th>부서</th>
                      <th>용도</th>
                      <th>공급자</th>
                      <th>모델</th>
                      <th className="num nowrap">호출</th>
                      <th className="num nowrap">실패·차단</th>
                      <th className="num nowrap">입력 토큰</th>
                      <th className="num nowrap">출력 토큰</th>
                      <th className="num nowrap">금액</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.rows.map((r, i) => (
                      <tr key={i}>
                        <td>{r.departmentName}</td>
                        <td>{purposeLabel(r.purpose)}</td>
                        <td>{r.provider}</td>
                        <td>{r.model}</td>
                        <td className="num">{r.calls.toLocaleString("ko-KR")}</td>
                        <td className="num">{r.failed.toLocaleString("ko-KR")}</td>
                        <td className="num">{r.inputTokens.toLocaleString("ko-KR")}</td>
                        <td className="num">{r.outputTokens.toLocaleString("ko-KR")}</td>
                        <td className="num">{money(r.costMicroUsd, r.costKrw, fx)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </TableWrap>
            </>
          )}

          <h2>월 환율과 실제 청구액</h2>
          <p>
            {fx > 0 ? `이 달 환율: 1달러 = ${fxText(fx)}원` : "이 달 환율이 아직 없습니다. 원화 금액은 환율을 입력한 뒤 계산됩니다."}
          </p>
          {data.adjustments.length > 0 && (
            <TableWrap>
              <table aria-label="입력한 청구액">
                <thead>
                  <tr>
                    <th>공급자</th>
                    <th className="num nowrap">청구액</th>
                    <th>메모</th>
                    <th>입력자</th>
                  </tr>
                </thead>
                <tbody>
                  {data.adjustments.map((a) => (
                    <tr key={a.id}>
                      <td>{a.provider}</td>
                      <td className="num">{won(a.actualKrw)}</td>
                      <td>{a.note || "-"}</td>
                      <td className="nowrap">{a.enteredByName || "-"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </TableWrap>
          )}
          {allocate ? (
            <>
              <form aria-label="환율 입력" className="form-row" onSubmit={saveFx}>
                <label>
                  1달러당 원 <input inputMode="decimal" value={fxInput} onChange={(e) => setFxInput(e.target.value)} placeholder="예: 1350.5" />
                </label>
                <button type="submit">환율 저장</button>
              </form>
              <form aria-label="청구액 입력" className="form-row" onSubmit={saveAdj}>
                <label>
                  공급자{" "}
                  <select value={adjProvider} onChange={(e) => setAdjProvider(e.target.value)}>
                    <option value="">선택</option>
                    {data.providers.map((p) => (
                      <option key={p.provider} value={p.provider}>
                        {p.provider}
                      </option>
                    ))}
                  </select>
                </label>
                <label>
                  실제 청구액(원) <input inputMode="numeric" value={adjAmount} onChange={(e) => setAdjAmount(e.target.value)} />
                </label>
                <label>
                  메모 <input value={adjNote} maxLength={200} onChange={(e) => setAdjNote(e.target.value)} />
                </label>
                <button type="submit">청구액 저장</button>
              </form>
            </>
          ) : (
            <Notice tone="info">환율과 청구액은 재무 담당이 입력합니다.</Notice>
          )}
        </>
      )}
    </div>
  );
}

export function AiCostMyPage() {
  const [month, setMonth] = useState(thisMonth());
  const [data, setData] = useState<MyReport | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      setData(await api.get<MyReport>(`/api/ai-cost/my?month=${encodeURIComponent(month)}`));
    } catch (e) {
      setError(errorText(e));
    } finally {
      setLoading(false);
    }
  }, [month]);

  useEffect(() => {
    void load();
  }, [load]);

  const fx = data?.fxUsdKrwX1000 ?? 0;
  const max = Math.max(1, ...(data?.trend ?? []).map((t) => t.costMicroUsd));

  return (
    <div>
      <h1>우리 부서 AI 비용</h1>
      <p>우리 부서가 쓴 AI 비용입니다. 프롬프트와 답변 내용은 이 화면에 없고, 호출 수와 금액만 보입니다.</p>
      <form
        aria-label="조회 월"
        className="form-row"
        onSubmit={(e) => {
          e.preventDefault();
          void load();
        }}
      >
        <label>
          월 <input type="month" value={month} onChange={(e) => setMonth(e.target.value)} required />
        </label>
      </form>
      {error && <Notice tone="error">{error}</Notice>}
      {loading && !data && <p>불러오는 중…</p>}
      {data && (
        <>
          <Warnings items={data.warnings} />
          <p>
            {data.departmentName} · {data.month} · 사용 <strong>{money(data.totalMicroUsd, data.totalKrw, fx)}</strong> · 호출 {data.calls.toLocaleString("ko-KR")}건
          </p>
          {data.forecast && (
            <Notice tone="info">
              이번 달 예상 {money(data.forecast.costMicroUsd, data.forecast.costKrw, fx)} (오늘까지 {data.forecast.elapsedDays}일 사용분으로 {data.forecast.daysInMonth}일을 어림한 값입니다).
            </Notice>
          )}
          {data.budget ? (
            <div>
              <p>
                이번 달 예산 대비: 사용 {won(data.budget.usedKrw)} / 예산 {won(data.budget.amountKrw)} ({data.budget.pct}%)
                {data.budget.fxEstimated ? " — 이 달 환율이 없어 이전 달 환율로 어림한 값입니다" : ""}
              </p>
              <progress aria-label="예산 사용률" value={Math.min(data.budget.usedKrw, data.budget.amountKrw)} max={Math.max(1, data.budget.amountKrw)} />
              {data.budget.pct >= 100 ? (
                <Notice tone="error">AI 비용이 이번 달 예산을 넘었습니다.</Notice>
              ) : data.budget.pct >= 80 ? (
                <Notice tone="warn">AI 비용이 예산의 {data.budget.pct}% 에 닿았습니다.</Notice>
              ) : null}
            </div>
          ) : (
            <Notice tone="info">이 부서는 이번 달 AI 비용 예산이 없습니다. 재무 담당에게 설정을 요청하세요.</Notice>
          )}
          <h2>최근 12개월</h2>
          <TableWrap>
            <table aria-label="월별 추이">
              <thead>
                <tr>
                  <th>월</th>
                  <th className="num nowrap">호출</th>
                  <th className="num nowrap">금액</th>
                  <th aria-label="막대" />
                </tr>
              </thead>
              <tbody>
                {data.trend.map((t) => (
                  <tr key={t.month}>
                    <td className="nowrap">{t.month}</td>
                    <td className="num">{t.calls.toLocaleString("ko-KR")}</td>
                    <td className="num">{usd(t.costMicroUsd)}{t.costKrw > 0 ? ` (${won(t.costKrw)})` : ""}</td>
                    <td>
                      <progress aria-label={`${t.month} 금액 비율`} value={t.costMicroUsd} max={max} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
          {data.rows.length === 0 ? (
            <EmptyState>이 달에는 우리 부서의 AI 사용 기록이 없습니다.</EmptyState>
          ) : (
            <>
              <h2>용도·공급자별</h2>
              <TableWrap>
                <table aria-label="용도별 AI 비용">
                  <thead>
                    <tr>
                      <th>용도</th>
                      <th>공급자</th>
                      <th className="num nowrap">호출</th>
                      <th className="num nowrap">금액</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.rows.map((r, i) => (
                      <tr key={i}>
                        <td>{purposeLabel(r.purpose)}</td>
                        <td>{r.provider}</td>
                        <td className="num">{r.calls.toLocaleString("ko-KR")}</td>
                        <td className="num">{money(r.costMicroUsd, r.costKrw, fx)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </TableWrap>
              <h2>많이 쓴 사람(상위 5명)</h2>
              <TableWrap>
                <table aria-label="상위 사용자">
                  <thead>
                    <tr>
                      <th>이름</th>
                      <th className="num nowrap">호출</th>
                      <th className="num nowrap">금액</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.topUsers.map((u, i) => (
                      <tr key={i}>
                        <td>{u.name || "-"}</td>
                        <td className="num">{u.calls.toLocaleString("ko-KR")}</td>
                        <td className="num">{money(u.costMicroUsd, u.costKrw, fx)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </TableWrap>
            </>
          )}
        </>
      )}
    </div>
  );
}
