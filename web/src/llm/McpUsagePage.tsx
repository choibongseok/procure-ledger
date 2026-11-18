// AI 연결 사용 현황 화면(usage:read): 도구별 호출·거부·지연, 개인 토큰별 사용량을 기간을 골라 표로 본다.
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { businessMessage } from "../purchase/useLoad";
import { ApiError, api } from "../api/client";
import { TOOL_LABELS } from "../admin/auditLabels";
import { Badge, EmptyState, Notice, TableWrap } from "../layout/ui";

export interface UsageStats {
  calls: number;
  success: number;
  denied: number;
  authFailed: number;
  rateLimited: number;
  errors: number;
  rejectRate: number;
  avgMs: number;
  p50Ms: number;
  p95Ms: number;
  maxMs: number;
}

export interface ToolUsage extends UsageStats {
  tool: string;
}

export interface TokenUsage extends UsageStats {
  tokenId: number;
  name: string;
  prefix: string;
  owner: string;
  revoked: boolean;
  lastAt: string;
}

export interface McpUsage {
  from: string;
  to: string;
  ratePerMin: number;
  ratePerUserPerMin?: number;
  total: UsageStats;
  tools: ToolUsage[];
  tokens: TokenUsage[];
}

/** 도구 이름을 화면용 한국어로 바꾼다(감사 화면 라벨 재사용). 모르는 값은 코드 대신 일반 문구로 보인다. */
export function toolLabel(tool: string): string {
  return TOOL_LABELS[tool] ?? "기타 도구";
}

export function limitText(perToken: number, perUser: number): string {
  const parts: string[] = [];
  if (perToken > 0) parts.push(`토큰당 분당 ${perToken}번`);
  if (perUser > 0) parts.push(`사용자당 분당 ${perUser}번(토큰 합산)`);
  return parts.length > 0 ? `${parts.join(", ")}까지 호출할 수 있습니다.` : "분당 호출 한도는 설정되어 있지 않습니다.";
}

export function formatRate(rate: number): string {
  return `${(rate * 100).toFixed(1)}%`;
}

export function formatMs(ms: number): string {
  return `${ms.toLocaleString("ko-KR")}ms`;
}

export function formatLastAt(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "-";
  return d.toLocaleString("ko-KR");
}

function dayString(d: Date): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

/** 기본 조회 기간: 오늘을 포함한 최근 7일. */
export function defaultPeriod(now: Date = new Date()): { from: string; to: string } {
  const from = new Date(now);
  from.setDate(from.getDate() - 6);
  return { from: dayString(from), to: dayString(now) };
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

function Cells({ s }: { s: UsageStats }) {
  return (
    <>
      <td className="num">{s.calls.toLocaleString("ko-KR")}</td>
      <td className="num">{s.success.toLocaleString("ko-KR")}</td>
      <td className="num">{s.denied + s.authFailed}</td>
      <td className="num">{s.rateLimited}</td>
      <td className="num">{s.errors}</td>
      <td className="num">{s.calls === 0 ? "-" : formatRate(s.rejectRate)}</td>
      <td className="num">{s.calls === 0 ? "-" : formatMs(s.avgMs)}</td>
      <td className="num">{s.calls === 0 ? "-" : formatMs(s.p95Ms)}</td>
      <td className="num">{s.calls === 0 ? "-" : formatMs(s.maxMs)}</td>
    </>
  );
}

function StatHead({ first }: { first: string[] }) {
  return (
    <tr>
      {first.map((h) => (
        <th key={h} className="nowrap">
          {h}
        </th>
      ))}
      <th className="num nowrap">호출</th>
      <th className="num nowrap">성공</th>
      <th className="num nowrap">권한·인증 거부</th>
      <th className="num nowrap">한도 초과</th>
      <th className="num nowrap">오류</th>
      <th className="num nowrap">거부율</th>
      <th className="num nowrap">평균 지연</th>
      <th className="num nowrap">느린 호출(상위 5%)</th>
      <th className="num nowrap">최대 지연</th>
    </tr>
  );
}

export function McpUsagePage() {
  const initial = defaultPeriod();
  const [from, setFrom] = useState(initial.from);
  const [to, setTo] = useState(initial.to);
  const [data, setData] = useState<McpUsage | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);

  const load = useCallback(async (f: string, t: string) => {
    setLoading(true);
    setError("");
    try {
      setData(await api.get<McpUsage>(`/api/mcp/usage?from=${encodeURIComponent(f)}&to=${encodeURIComponent(t)}`));
    } catch (e) {
      setError(errorText(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load(initial.from, initial.to);
    // 처음 한 번만 기본 기간으로 불러온다.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [load]);

  function submit(e: FormEvent) {
    e.preventDefault();
    void load(from, to);
  }

  return (
    <div>
      <h1>AI 연결 사용 현황</h1>
      <p>AI 도구가 ERP 를 호출한 기록을 감사 로그에서 모아 보여 줍니다. 조회는 기록을 남기지 않습니다.</p>
      <form onSubmit={submit} aria-label="조회 기간" className="form-row">
        <label>
          시작일 <input type="date" value={from} onChange={(e) => setFrom(e.target.value)} required />
        </label>
        <label>
          종료일 <input type="date" value={to} onChange={(e) => setTo(e.target.value)} required />
        </label>
        <button type="submit">조회</button>
      </form>
      {error && <Notice tone="error">{error}</Notice>}
      {loading && !data && <p>불러오는 중…</p>}
      {data && (
        <>
          <p>
            {data.from} ~ {data.to} · 전체 호출 {data.total.calls.toLocaleString("ko-KR")}건 · 거부율{" "}
            {data.total.calls === 0 ? "-" : formatRate(data.total.rejectRate)} ·{" "}
            {limitText(data.ratePerMin, data.ratePerUserPerMin ?? 0)}
          </p>

          <h2>도구별 사용량</h2>
          {data.tools.length === 0 ? (
            <EmptyState>이 기간에 호출 기록이 없습니다.</EmptyState>
          ) : (
            <TableWrap>
            <table aria-label="도구별 사용량">
              <thead>
                <StatHead first={["도구"]} />
              </thead>
              <tbody>
                {data.tools.map((t) => (
                  <tr key={t.tool}>
                    <td className="nowrap">{toolLabel(t.tool)}</td>
                    <Cells s={t} />
                  </tr>
                ))}
              </tbody>
            </table>
            </TableWrap>
          )}

          <h2>토큰별 사용량</h2>
          <Notice tone="info">
            아예 없는 값이나 잘못된 토큰으로 접속한 요청은 연결 단계에서 거절되어 기록되지 않습니다. 이미 연결된 상태에서 토큰이 폐기·만료된 뒤의
            호출만 아래에 &quot;폐기·만료된 토큰으로 호출한 기록&quot; 한 줄로 모입니다.
          </Notice>
          {data.tokens.length === 0 ? (
            <EmptyState>이 기간에 호출 기록이 없습니다.</EmptyState>
          ) : (
            <TableWrap>
            <table aria-label="토큰별 사용량">
              <thead>
                <StatHead first={["토큰", "소유자", "마지막 호출"]} />
              </thead>
              <tbody>
                {data.tokens.map((t) => (
                  <tr key={t.tokenId}>
                    <td>
                      {t.tokenId === 0 ? "폐기·만료된 토큰으로 호출한 기록" : `${t.name || "이름 없음"} (${t.prefix}…)`}
                      {t.tokenId !== 0 && t.revoked && (
                        <>
                          {" "}
                          <Badge tone="muted">폐기됨</Badge>
                        </>
                      )}
                    </td>
                    <td className="nowrap">{t.owner || "-"}</td>
                    <td className="nowrap">{formatLastAt(t.lastAt)}</td>
                    <Cells s={t} />
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
