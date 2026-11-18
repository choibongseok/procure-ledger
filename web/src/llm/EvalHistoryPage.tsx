// 평가 이력 화면(eval:read): 실행마다 합격률과 새로 실패·새로 통과한 케이스를 표로 보여 준다.
import { useEffect, useState } from "react";
import { api } from "../api/client";
import { Badge, EmptyState, Notice, TableWrap } from "../layout/ui";

export interface EvalCase {
  suite: string;
  id: string;
  pass: boolean;
  critical: boolean;
  detail: string;
}

export interface EvalRun {
  id: number;
  at: string;
  provider: string;
  model: string;
  total: number;
  passed: number;
  passRatePct: number;
  thresholdPct: number;
  gatePassed: boolean;
  newFailures: string[];
  newPasses: string[];
  cases: EvalCase[];
}

const SUITE_LABEL: Record<string, string> = { docs: "문서 판정", tools: "도구 선택" };
const PROVIDER_LABEL: Record<string, string> = { mock: "시험용 모의 응답", anthropic: "Claude", bedrock: "AWS Bedrock", vertex: "Google Vertex AI", openai: "OpenAI 호환" };

/** 실행 시각(UTC 문자열)을 다른 화면과 같은 한국어 지역 표기로 바꾼다. 읽을 수 없으면 "-". */
export function formatRunAt(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "-" : d.toLocaleString("ko-KR");
}

/** "docs/match-basic" 같은 케이스 식별자를 "문서 판정 · match-basic" 으로 바꾼다. */
export function caseLabel(key: string): string {
  const i = key.indexOf("/");
  if (i < 0) return key;
  return `${SUITE_LABEL[key.slice(0, i)] ?? "기타"} · ${key.slice(i + 1)}`;
}

export interface CompareRun {
  id: number;
  provider: string;
  model: string;
  passRatePct: number;
  criticalFailures: number;
  regressions: number;
  criticalRegressions: number;
  latencyMs: number;
  costMicroUsd: number;
  reference: boolean;
}

export interface Comparison {
  runs: CompareRun[];
  cases: { key: string; critical: boolean; results: string[] }[];
  excluded: number;
  warning: string;
}

/** 공급자·모델별 최신 실행을 같은 골든셋끼리 케이스 × 공급자 표로 비교한다(참고용, 차단 없음). */
export function ProviderCompare() {
  const [cmp, setCmp] = useState<Comparison | null>(null);
  useEffect(() => {
    api
      .get<Comparison>("/api/eval/compare")
      .then(setCmp)
      .catch(() => setCmp(null));
  }, []);
  if (!cmp || !Array.isArray(cmp.runs) || !Array.isArray(cmp.cases)) return null;
  if (cmp.runs.length < 2) {
    return (
      <section className="card" aria-labelledby="eval-compare">
        <h2 id="eval-compare">공급자 비교</h2>
        <p className="hint">같은 골든셋으로 돌린 서로 다른 공급자·모델의 실행이 둘 이상 있으면 비교 표가 나옵니다.</p>
      </section>
    );
  }
  const label = (r: CompareRun) => `${providerLabel(r.provider)}${r.model ? ` (${r.model})` : ""}${r.reference ? " · 기준" : ""}`;
  return (
    <section className="card" aria-labelledby="eval-compare">
      <h2 id="eval-compare">공급자 비교</h2>
      <p className="hint">같은 골든셋으로 돌린 공급자·모델별 최신 실행입니다. 첫 열이 기준이고, 기준에서 통과하던 케이스가 다른 곳에서 실패하면 회귀로 표시합니다. 참고용이며 저장·적용을 막지 않습니다.</p>
      {cmp.warning && <Notice tone="warn">{cmp.warning}</Notice>}
      {cmp.excluded > 0 && <p className="hint">골든셋이 달라 표에서 뺀 실행이 {cmp.excluded}개 있습니다.</p>}
      <TableWrap>
        <table aria-label="공급자 비교 표">
          <thead>
            <tr>
              <th>케이스</th>
              {cmp.runs.map((r) => (
                <th key={r.id} className="nowrap">
                  {label(r)}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {cmp.cases.map((c) => (
              <tr key={c.key}>
                <td>
                  {caseLabel(c.key)} {c.critical && <Badge tone="warn">치명</Badge>}
                </td>
                {c.results.map((res, i) => (
                  <td key={i} className="nowrap">
                    {res === "pass" ? "통과" : res === "fail" ? i > 0 && c.results[0] === "pass" ? <Badge tone="bad">실패(회귀)</Badge> : "실패" : "-"}
                  </td>
                ))}
              </tr>
            ))}
            <tr>
              <th>합격률</th>
              {cmp.runs.map((r) => (
                <td key={r.id} className="num">
                  {r.passRatePct}%
                </td>
              ))}
            </tr>
            <tr>
              <th>치명 케이스 실패</th>
              {cmp.runs.map((r) => (
                <td key={r.id} className="num">
                  {r.criticalFailures}
                </td>
              ))}
            </tr>
            <tr>
              <th>지연 합(ms)</th>
              {cmp.runs.map((r) => (
                <td key={r.id} className="num">
                  {r.latencyMs.toLocaleString("ko-KR")}
                </td>
              ))}
            </tr>
          </tbody>
        </table>
      </TableWrap>
    </section>
  );
}

export function providerLabel(p: string): string {
  return PROVIDER_LABEL[p] ?? p;
}

/** 실행 결과 판정: 기준 통과 / 합격률은 넘었으나 회귀 / 기준 미달. */
export function runStatus(r: EvalRun): string {
  if (r.gatePassed) return "통과";
  if (r.newFailures.length > 0) return "회귀 감지";
  return "기준 미달";
}

function CaseTable({ cases }: { cases: EvalCase[] }) {
  return (
    <TableWrap>
    <table aria-label="케이스별 결과">
      <thead>
        <tr>
          <th>구분</th>
          <th>케이스</th>
          <th>결과</th>
          <th>비고</th>
        </tr>
      </thead>
      <tbody>
        {cases.map((c) => (
          <tr key={c.suite + "/" + c.id}>
            <td>{SUITE_LABEL[c.suite] ?? "기타"}</td>
            <td>{c.id}</td>
            <td className="nowrap">
              <Badge tone={c.pass ? "ok" : "bad"}>
                {c.pass ? "통과" : "실패"}
                {c.critical ? "(필수)" : ""}
              </Badge>
            </td>
            <td>{c.detail}</td>
          </tr>
        ))}
      </tbody>
    </table>
    </TableWrap>
  );
}

export function EvalHistoryPage() {
  const [runs, setRuns] = useState<EvalRun[] | null>(null);
  const [error, setError] = useState("");
  const [open, setOpen] = useState(0);

  useEffect(() => {
    api
      .get<{ runs: EvalRun[] }>("/api/eval/runs")
      .then((r) => setRuns(r.runs))
      .catch(() => setError("평가 이력을 불러오지 못했습니다."));
  }, []);

  return (
    <section>
      <h1>평가 이력</h1>
      <p>AI 연결 품질을 골든셋으로 측정한 결과입니다. 이전 기준보다 새로 틀리게 된 케이스가 있으면 회귀로 표시합니다.</p>
      {error && <Notice tone="error">{error}</Notice>}
      {runs === null && !error && <p>불러오는 중…</p>}
      {runs !== null && runs.length === 0 && <EmptyState>저장된 평가 실행이 없습니다.</EmptyState>}
      {runs !== null && runs.length > 0 && (
        <TableWrap>
        <table aria-label="평가 실행 이력">
          <thead>
            <tr>
              <th className="nowrap">실행 시각</th>
              <th className="nowrap">응답 방식</th>
              <th className="nowrap">모델</th>
              <th className="num nowrap">합격률</th>
              <th className="nowrap">판정</th>
              <th>새로 실패</th>
              <th>새로 통과</th>
              <th>상세</th>
            </tr>
          </thead>
          <tbody>
            {runs.map((r) => (
              <tr key={r.id}>
                <td className="nowrap">{formatRunAt(r.at)}</td>
                <td className="nowrap">{providerLabel(r.provider)}</td>
                <td className="nowrap">{r.model || "-"}</td>
                <td className="num nowrap">
                  {r.passRatePct}% ({r.passed}/{r.total})
                </td>
                <td className="nowrap">
                  <Badge tone={r.gatePassed ? "ok" : "bad"}>{runStatus(r)}</Badge>
                </td>
                <td>{r.newFailures.length ? r.newFailures.map(caseLabel).join(", ") : "없음"}</td>
                <td>{r.newPasses.length ? r.newPasses.map(caseLabel).join(", ") : "없음"}</td>
                <td className="nowrap">
                  <button type="button" className="btn-secondary btn-sm" aria-expanded={open === r.id} onClick={() => setOpen(open === r.id ? 0 : r.id)}>
                    케이스 {open === r.id ? "닫기" : "보기"}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        </TableWrap>
      )}
      {runs?.filter((r) => r.id === open).map((r) => <CaseTable key={r.id} cases={r.cases} />)}
      <ProviderCompare />
    </section>
  );
}
