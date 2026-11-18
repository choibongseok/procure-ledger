// 기초 데이터 대조 보고서 화면(B35 3.9): 원 시스템 수치를 입력하면 새 시스템 값(코드가 집계)과 나란히 보인다. 읽기 전용, 입력값은 저장하지 않는다.
import { useState } from "react";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { ExportButtons } from "../layout/ExportButtons";
import { Badge, EmptyState, Notice, TableWrap } from "../layout/ui";
import { won } from "../purchase/labels";
import { errorText, useLoad } from "../purchase/useLoad";

interface Row {
  code: string;
  label: string;
  original: number | null;
  system: number;
  diff: number | null;
  verdict: "match" | "diff" | "";
  verdictLabel: string;
  notes: string[];
}
interface Report {
  rows: Row[];
  pendingOpeningCount: number;
}

const CODES: { code: string; label: string; money: boolean }[] = [
  { code: "accounts", label: "계정과목 수", money: false },
  { code: "vendors", label: "거래처 수", money: false },
  { code: "items", label: "품목 수", money: false },
  { code: "stock_qty", label: "재고 수량 합", money: false },
  { code: "stock_value", label: "재고 금액 합(원)", money: true },
  { code: "payable", label: "미지급 잔액 합(원)", money: true },
  { code: "budget", label: "부서 예산 합(원)", money: true },
];

const num = (n: number, money: boolean) => (money ? won(n) : n.toLocaleString("ko-KR"));

export function ReconcilePage() {
  const { entities, currentEntity } = useAuth();
  const stockEntity = (entities?.length ?? 0) > 1 ? (currentEntity?.all ? "전체 법인" : currentEntity?.name) : undefined;
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [applied, setApplied] = useState<Record<string, string>>({});
  const [problem, setProblem] = useState("");
  const qs = new URLSearchParams(Object.entries(applied).filter(([, v]) => v !== "")).toString();
  const rep = useLoad(() => api.get<Report>(`/api/imports/reconcile${qs ? `?${qs}` : ""}`), [qs]);

  function run() {
    const clean: Record<string, string> = {};
    for (const [k, v] of Object.entries(draft)) {
      const t = v.replace(/[,\s원]/g, "");
      if (t === "") continue;
      if (!/^\d+$/.test(t)) {
        setProblem("원 시스템 수치는 0 이상의 정수로 입력하세요(쉼표는 괜찮습니다).");
        return;
      }
      clean[k] = t;
    }
    setProblem("");
    setApplied(clean);
  }

  const r = rep.data;
  return (
    <section>
      <h1>기초 데이터 대조</h1>
      <p className="hint">가져오기를 마친 뒤, 원래 시스템(엑셀 등)의 수치를 입력하면 새 시스템 값과 비교합니다. 새 시스템 값은 이 화면이 DB 에서 직접 집계합니다. 입력한 수치는 저장하지 않습니다.</p>
      <div className="form-row">
        {CODES.map((c) => (
          <div className="field" key={c.code}>
            <label htmlFor={`rc-${c.code}`}>{c.label}</label>
            <input id={`rc-${c.code}`} inputMode="numeric" value={draft[c.code] ?? ""} onChange={(e) => setDraft({ ...draft, [c.code]: e.target.value })} />
          </div>
        ))}
        <button type="button" onClick={run}>
          대조하기
        </button>
        <ExportButtons path="/api/imports/reconcile/export" params={applied} disabled={rep.loading || !r} />
      </div>
      {stockEntity && <p className="hint">재고는 {stockEntity}만 대조합니다.</p>}
      {problem && <Notice tone="error">{problem}</Notice>}
      {rep.error && <Notice tone="error">{rep.error || errorText(rep.error)}</Notice>}
      {r && r.pendingOpeningCount > 0 && (
        <Notice tone="warn">승인 전 기초 전표가 {r.pendingOpeningCount}건 있습니다. 기초 재고의 수량·금액은 이미 재고에 들어갔지만 계정별 원장·시산표에는 승인 뒤에 들어갑니다.</Notice>
      )}
      {r && r.rows.length === 0 && <EmptyState>집계할 항목이 없습니다.</EmptyState>}
      {r && r.rows.length > 0 && (
        <TableWrap>
          <table className="stack-table" aria-label="기초 데이터 대조">
            <thead>
              <tr>
                <th>항목</th>
                <th className="num nowrap">원 시스템</th>
                <th className="num nowrap">새 시스템</th>
                <th className="num nowrap">차이</th>
                <th className="nowrap">판정</th>
                <th>참고</th>
              </tr>
            </thead>
            <tbody>
              {r.rows.map((x) => {
                const money = CODES.find((c) => c.code === x.code)?.money ?? false;
                return (
                  <tr key={x.code}>
                    <td data-label="항목">{x.label}</td>
                    <td data-label="원 시스템" className="num">{x.original === null ? "—" : num(x.original, money)}</td>
                    <td data-label="새 시스템" className="num">{num(x.system, money)}</td>
                    <td data-label="차이" className="num">{x.diff === null ? "—" : num(x.diff, money)}</td>
                    <td data-label="판정" className="nowrap">{x.verdict === "" ? "—" : <Badge tone={x.verdict === "match" ? "ok" : "bad"}>{x.verdictLabel}</Badge>}</td>
                    <td data-label="참고">{x.notes.length ? x.notes.join(" ") : "—"}</td>
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
