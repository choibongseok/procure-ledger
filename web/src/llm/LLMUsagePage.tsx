// AI 사용량 화면: 일·역할·모델·용도별 사용량과 비용, 권한이 있으면 모델 허용 규칙·일일 한도 현황(읽기 전용)을 본다. 편집은 관리자 설정(platform) 화면.
import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { businessMessage } from "../purchase/useLoad";
import { ApiError, api } from "../api/client";
import { PermissionGate } from "../auth/PermissionGate";
import { roleName, useRoleOptions } from "../admin/roleNames";
import { Badge, EmptyState, Notice, TableWrap } from "../layout/ui";

export interface UsageRow {
  /** 최종 응답한 공급자 코드(B45). 이전 서버 응답에는 없다. */
  provider?: string;
  /** 대체 공급자로 응답한 호출 수(B45). */
  fallbacks?: number;
  day: string;
  role: string;
  model: string;
  purpose: string;
  calls: number;
  blocked: number;
  inputTokens: number;
  outputTokens: number;
  costMicroUsd: number;
  maskedCount: number;
}

/** 역할별 오늘 사용량과 하루 한도(GET /api/llm/usage/limits). 한도가 없으면 dailyTokenLimit 이 null. */
export interface RoleLimit {
  role: string;
  usedTokens: number;
  dailyTokenLimit: number | null;
}

export interface ModelTotal {
  model: string;
  calls: number;
  blocked: number;
  inputTokens: number;
  outputTokens: number;
  costMicroUsd: number;
}

/** 일별 표의 같은 모델 행을 더한다. 처음 나온 순서를 지킨다. */
export function modelTotals(rows: UsageRow[]): ModelTotal[] {
  const by = new Map<string, ModelTotal>();
  for (const r of rows) {
    const t = by.get(r.model) ?? { model: r.model, calls: 0, blocked: 0, inputTokens: 0, outputTokens: 0, costMicroUsd: 0 };
    t.calls += r.calls;
    t.blocked += r.blocked;
    t.inputTokens += r.inputTokens;
    t.outputTokens += r.outputTokens;
    t.costMicroUsd += r.costMicroUsd;
    by.set(r.model, t);
  }
  return [...by.values()];
}

/** 사용률(%, 반올림). 한도가 없거나 0 이면 null. */
export function usageRate(used: number, limit: number | null): number | null {
  if (limit === null || limit <= 0) return null;
  return Math.round((used * 100) / limit);
}

/** 한도의 80% 이상이면 주의 대상(서버의 "한도에 가까움" 기준과 같다). */
export function isNearLimit(r: RoleLimit): boolean {
  return r.dailyTokenLimit !== null && r.dailyTokenLimit > 0 && r.usedTokens * 100 >= r.dailyTokenLimit * 80;
}

export function isOverLimit(r: RoleLimit): boolean {
  return r.dailyTokenLimit !== null && r.dailyTokenLimit > 0 && r.usedTokens >= r.dailyTokenLimit;
}

const LOAD_ERROR = "AI 사용량을 불러오지 못했습니다. 잠시 뒤 다시 시도하세요.";

interface Policy {
  id: number;
  role: string;
  departmentId: number;
  modelPattern: string;
  allowed: boolean;
}

interface Quota {
  id: number;
  scopeType: string;
  scopeValue: string;
  dailyTokenLimit: number;
}

const SCOPE_LABEL: Record<string, string> = { role: "역할", department: "부서", user: "사용자" };

export function formatCost(micro: number): string {
  return `$${(micro / 1_000_000).toFixed(4)}`;
}

const PURPOSE_LABEL: Record<string, string> = {
  "docverify.extract": "증빙 문서 읽기",
  "mcp.select_tool": "도구 선택",
};

/** 용도 코드를 화면용 한국어로 바꾼다. 모르는 값은 코드 대신 일반 문구로 보인다. */
export function purposeLabel(purpose: string): string {
  return PURPOSE_LABEL[purpose] ?? "기타 용도";
}

/** 하루 한도 입력("1,000" 처럼 쉼표·공백이 든 숫자 포함)을 정수로 바꾼다. 숫자가 아니면 null. */
export function parseTokenLimit(input: string): number | null {
  const t = input.replace(/[,\s]/g, "");
  if (!/^\d+$/.test(t)) return null;
  const n = Number(t);
  return Number.isSafeInteger(n) ? n : null;
}

const roleLabel = roleName;

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

function TodayLimits({ roles }: { roles: RoleLimit[] }) {
  const near = roles.filter(isNearLimit);
  return (
    <section aria-label="오늘 사용량과 일일 한도">
      {near.map((r) => (
        <Notice key={r.role} tone="warn" testId="near-limit">
          {`오늘 한도의 80% 를 넘었습니다(${roleLabel(r.role)} ${r.usedTokens.toLocaleString("ko-KR")} / ${(r.dailyTokenLimit ?? 0).toLocaleString("ko-KR")}). 한도에 닿으면 증빙 판정과 도구 선택이 멈춥니다.`}
        </Notice>
      ))}
      <h2>오늘 사용량과 일일 한도</h2>
      <p className="hint">개인·부서 한도가 따로 있으면 그 한도가 먼저 적용됩니다.</p>
      {roles.length === 0 ? (
        <EmptyState>오늘 AI 사용 기록과 설정된 한도가 없습니다.</EmptyState>
      ) : (
        <TableWrap>
          <table aria-label="오늘 사용량과 일일 한도">
            <thead>
              <tr>
                <th className="nowrap">역할</th>
                <th className="num nowrap">오늘 사용(입력+출력 토큰)</th>
                <th className="num nowrap">하루 한도(토큰)</th>
                <th className="num nowrap">사용률</th>
              </tr>
            </thead>
            <tbody>
              {roles.map((r) => {
                const rate = usageRate(r.usedTokens, r.dailyTokenLimit);
                return (
                  <tr key={r.role}>
                    <td className="nowrap">{roleLabel(r.role)}</td>
                    <td className="num">{r.usedTokens.toLocaleString("ko-KR")}</td>
                    <td className="num">{r.dailyTokenLimit === null ? "한도 없음" : r.dailyTokenLimit.toLocaleString("ko-KR")}</td>
                    <td className="num nowrap">
                      {rate === null ? "-" : `${rate}%`}
                      {isOverLimit(r) && (
                        <>
                          {" "}
                          <Badge tone="bad">한도 초과</Badge>
                        </>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </TableWrap>
      )}
    </section>
  );
}

function ModelTable({ rows }: { rows: UsageRow[] }) {
  const totals = modelTotals(rows);
  return (
    <section aria-label="모델별 사용량">
      <h2>모델별 사용량</h2>
      <TableWrap>
        <table aria-label="모델별 사용량">
          <thead>
            <tr>
              <th className="nowrap">모델</th>
              <th className="num nowrap">호출</th>
              <th className="num nowrap">차단</th>
              <th className="num nowrap">입력 토큰</th>
              <th className="num nowrap">출력 토큰</th>
              <th className="num nowrap">비용</th>
            </tr>
          </thead>
          <tbody>
            {totals.map((t) => (
              <tr key={t.model}>
                <td className="nowrap">{t.model}</td>
                <td className="num">{t.calls}</td>
                <td className="num">{t.blocked}</td>
                <td className="num">{t.inputTokens.toLocaleString("ko-KR")}</td>
                <td className="num">{t.outputTokens.toLocaleString("ko-KR")}</td>
                <td className="num">{formatCost(t.costMicroUsd)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>
    </section>
  );
}

function UsageTable({ rows }: { rows: UsageRow[] }) {
  return (
    <TableWrap>
    <table>
      <thead>
        <tr>
          <th className="nowrap">날짜</th>
          <th className="nowrap">역할</th>
          <th className="nowrap">공급자</th>
          <th className="nowrap">모델</th>
          <th className="nowrap">용도</th>
          <th className="num nowrap">호출</th>
          <th className="nowrap">대체</th>
          <th className="num nowrap">차단</th>
          <th className="num nowrap">입력</th>
          <th className="num nowrap">출력</th>
          <th className="num nowrap">비용</th>
          <th className="num nowrap">가린 항목</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((r, i) => (
          <tr key={i}>
            <td className="nowrap">{r.day}</td>
            <td className="nowrap">{roleLabel(r.role)}</td>
            <td className="nowrap">{r.provider || "-"}</td>
            <td className="nowrap">{r.model}</td>
            <td className="nowrap">{purposeLabel(r.purpose)}</td>
            <td className="num">{r.calls}</td>
            <td className="nowrap">{r.fallbacks ? <Badge tone="warn">{`대체 ${r.fallbacks}건`}</Badge> : "-"}</td>
            <td className="num">{r.blocked}</td>
            <td className="num">{r.inputTokens.toLocaleString("ko-KR")}</td>
            <td className="num">{r.outputTokens.toLocaleString("ko-KR")}</td>
            <td className="num">{formatCost(r.costMicroUsd)}</td>
            <td className="num">{r.maskedCount}</td>
          </tr>
        ))}
      </tbody>
    </table>
    </TableWrap>
  );
}

function PolicyStatus() {
  const [policies, setPolicies] = useState<Policy[]>([]);
  const [quotas, setQuotas] = useState<Quota[]>([]);
  const [error, setError] = useState("");

  useEffect(() => {
    (async () => {
      try {
        setPolicies((await api.get<{ policies: Policy[] }>("/api/llm/policies")).policies);
        setQuotas((await api.get<{ quotas: Quota[] }>("/api/llm/quotas")).quotas);
      } catch (e) {
        setError(errorText(e));
      }
    })();
  }, []);

  return (
    <section aria-label="모델 정책 현황">
      <h2>사용 가능한 모델</h2>
      {error && <Notice tone="error">{error}</Notice>}
      {policies.length === 0 ? (
        <EmptyState>등록된 모델 규칙이 없습니다. 규칙이 하나도 없으면 모든 호출이 거부됩니다.</EmptyState>
      ) : (
        <TableWrap>
          <table aria-label="모델 규칙">
            <thead>
              <tr>
                <th className="nowrap">역할</th>
                <th className="nowrap">부서</th>
                <th className="nowrap">모델</th>
                <th className="nowrap">허용 여부</th>
              </tr>
            </thead>
            <tbody>
              {policies.map((p) => (
                <tr key={p.id}>
                  <td className="nowrap">{roleLabel(p.role)}</td>
                  <td className="nowrap">{p.departmentId ? `부서 ${p.departmentId}` : "전체"}</td>
                  <td className="nowrap">{p.modelPattern}</td>
                  <td className="nowrap">
                    <Badge tone={p.allowed ? "ok" : "bad"}>{p.allowed ? "허용" : "금지"}</Badge>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}

      <h2>일일 사용 한도</h2>
      {quotas.length === 0 ? (
        <EmptyState>설정된 일일 한도가 없습니다.</EmptyState>
      ) : (
        <TableWrap>
          <table aria-label="일일 사용 한도">
            <thead>
              <tr>
                <th className="nowrap">범위</th>
                <th className="nowrap">대상</th>
                <th className="num nowrap">하루 한도(토큰)</th>
              </tr>
            </thead>
            <tbody>
              {quotas.map((q) => (
                <tr key={q.id}>
                  <td className="nowrap">{SCOPE_LABEL[q.scopeType] ?? "기타"}</td>
                  <td className="nowrap">{q.scopeType === "role" ? roleLabel(q.scopeValue) : q.scopeValue}</td>
                  <td className="num">{q.dailyTokenLimit.toLocaleString("ko-KR")}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      <p className="hint">
        모델 허용 규칙과 일일 한도는 <Link to="/admin/settings">관리자 설정</Link>에서 고칠 수 있습니다.
      </p>
    </section>
  );
}

export function LLMUsagePage() {
  useRoleOptions(); // 사용자 정의 역할 이름을 받아 둔다(표 안의 roleName 이 읽는다)
  const [rows, setRows] = useState<UsageRow[] | null>(null);
  const [roles, setRoles] = useState<RoleLimit[]>([]);
  const [error, setError] = useState("");
  useEffect(() => {
    api
      .get<{ rows: UsageRow[] }>("/api/llm/usage?days=7")
      .then((r) => setRows(r.rows ?? []))
      .catch(() => setError(LOAD_ERROR));
    // 한도 표는 보조 정보다. 실패해도 사용 내역은 보인다.
    api
      .get<{ roles: RoleLimit[] }>("/api/llm/usage/limits")
      .then((r) => setRoles(r.roles ?? []))
      .catch(() => setRoles([]));
  }, []);
  return (
    <section>
      <h1>AI 사용량</h1>
      <p>최근 7일 기준입니다. AI 로 보내는 글에서 사업자번호·계좌·연락처는 자동으로 가려집니다.</p>
      {error && <Notice tone="error">{error}</Notice>}
      {rows === null && !error && <p>불러오는 중…</p>}
      {rows && <TodayLimits roles={roles} />}
      {rows && rows.length === 0 && <EmptyState>최근 7일 동안 AI 사용 기록이 없습니다.</EmptyState>}
      {rows && rows.length > 0 && (
        <>
          <ModelTable rows={rows} />
          <h2>일별 사용 내역</h2>
          <UsageTable rows={rows} />
        </>
      )}
      <PermissionGate permission="llm:policy_manage">
        <PolicyStatus />
      </PermissionGate>
    </section>
  );
}
