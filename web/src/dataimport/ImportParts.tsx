// 기초 데이터 가져오기 공통 화면 부품(B35): 건수 요약, 행 표. 대상별 화면과 이력 상세가 함께 쓴다.
import { Badge, EmptyState, TableWrap, type Tone } from "../layout/ui";
import { VERDICT_LABEL, type Counts, type RowVerdict } from "./api";

export const STEPS = ["양식 내려받기", "파일 올리기", "미리보기", "반영 확인"] as const;

/** 전체·추가·변경·동일·오류·경고 건수. */
export function CountSummary({ counts }: { counts: Counts }) {
  const items: [string, number, Tone][] = [
    ["전체", counts.total, "muted"],
    ["추가", counts.add, "ok"],
    ["변경", counts.change, "info"],
    ["동일", counts.same, "muted"],
    ["오류", counts.error, counts.error > 0 ? "bad" : "muted"],
    ["경고", counts.warn, counts.warn > 0 ? "warn" : "muted"],
  ];
  return (
    <p className="chip-row" aria-label="건수 요약">
      {items.map(([label, n, tone]) => (
        <Badge key={label} tone={tone}>
          {label} {n.toLocaleString("ko-KR")}건
        </Badge>
      ))}
    </p>
  );
}

const VERDICT_TONE: Record<RowVerdict["verdict"], Tone> = { add: "ok", change: "info", same: "muted", error: "bad" };

/** 행 표(상위 일부). 바뀌는 칸은 이전 → 새 값으로 보인다. */
export function RowsTable({ rows, truncated, truncatedHint }: { rows: RowVerdict[]; truncated: boolean; truncatedHint: string }) {
  if (rows.length === 0) return <EmptyState>표시할 행이 없습니다.</EmptyState>;
  return (
    <>
      <TableWrap>
        <table>
          <thead>
            <tr>
              <th className="num">행</th>
              <th>판정</th>
              <th>키</th>
              <th>내용</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.rowNo}>
                <td className="num">{r.rowNo}</td>
                <td className="nowrap">
                  <Badge tone={VERDICT_TONE[r.verdict]}>{VERDICT_LABEL[r.verdict]}</Badge>
                </td>
                <td>{r.key}</td>
                <td>
                  {r.reason && <div>{r.reason}</div>}
                  {(r.changes ?? []).map((c) => (
                    <div key={c.column}>
                      {c.column}: {c.old || "(빈 값)"} → {c.new || "(빈 값)"}
                    </div>
                  ))}
                  {r.warn && <div className="hint">경고: {r.warn}</div>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>
      {truncated && <p className="hint">{truncatedHint}</p>}
    </>
  );
}
