// 구매요청 작성 화면의 "결재선 미리보기": 조직도로 만든 결재선(사람·직책·조직)과 못 만들 때의 단계별 사유(B21 7.1).
// 상신이 쓰는 서버의 결재선 만들기와 같은 함수가 돌려준 결과를 그대로 보인다.
import { useEffect, useState } from "react";
import { api } from "../api/client";
import { Badge, Notice, TableWrap } from "../layout/ui";
import type { LinePreview } from "./types";

const DEBOUNCE_MS = 400;

/** 미리보기 상태: null 이면 아직 모름(입력 전·불러오는 중·조회 실패), true/false 는 서버가 판정한 상신 가능 여부. */
export type LineReady = boolean | null;

/** 결재자가 부재 중일 때의 안내: "부재 중(~11. 24.) · 대결 ○○". 결재선(배정자)은 바뀌지 않는다. */
export function AbsenceNote({ until, delegate }: { until: string; delegate?: string }) {
  const d = new Date(until);
  const kst = new Date(d.getTime() + 9 * 3600 * 1000);
  return (
    <div className="hint">
      부재 중(~{kst.getUTCMonth() + 1}. {kst.getUTCDate()}.){delegate ? ` · 대결 ${delegate}` : ""}
    </div>
  );
}

export function LinePreviewCard({ amount, onReady }: { amount: number; onReady?: (ready: LineReady) => void }) {
  const [preview, setPreview] = useState<LinePreview | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    if (!(amount > 0)) {
      setPreview(null);
      setFailed(false);
      onReady?.(null);
      return;
    }
    let stale = false;
    const timer = setTimeout(() => {
      api
        .get<LinePreview>(`/api/approvals/line-preview?amount=${Math.round(amount)}`)
        .then((p) => {
          if (stale) return;
          setPreview(p);
          setFailed(false);
          onReady?.(p.complete);
        })
        .catch(() => {
          if (stale) return;
          setPreview(null);
          setFailed(true);
          onReady?.(null);
        });
    }, DEBOUNCE_MS);
    return () => {
      stale = true;
      clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [amount]);

  return (
    <aside className="card" aria-label="결재선 미리보기">
      <h2>결재선 미리보기</h2>
      {!(amount > 0) && <p>금액을 입력하면 결재선이 나타납니다.</p>}
      {failed && (
        <Notice tone="warn">결재선을 불러오지 못했습니다. 잠시 뒤 다시 시도해 주세요. 상신 때 서버가 다시 확인합니다.</Notice>
      )}
      {preview && (
        <>
          <p className="hint">요청자 소속: {preview.unitPath || "—"}. 상신하는 시점의 조직 기준으로 다시 만들어집니다.</p>
          {preview.complete && preview.steps.length > 0 && <LineTable steps={preview.steps.filter((s) => !s.omitted)} />}
          {preview.complete ? (
            <ul className="hint">
              {preview.notes.map((n) => (
                <li key={n}>{n}</li>
              ))}
            </ul>
          ) : (
            <Notice tone="error">
              <strong>이 상태로는 상신할 수 없습니다. 임시저장은 할 수 있습니다.</strong>
              <br />
              {preview.problems.map((p) => (
                <span key={p}>
                  {p}
                  <br />
                </span>
              ))}
            </Notice>
          )}
        </>
      )}
    </aside>
  );
}

function LineTable({ steps }: { steps: LinePreview["steps"] }) {
  return (
    <TableWrap>
      <table>
        <thead>
          <tr>
            <th className="nowrap">단계</th>
            <th className="nowrap">직책</th>
            <th>결재자</th>
            <th>소속 조직</th>
            <th>비고</th>
          </tr>
        </thead>
        <tbody>
          {steps.map((s) => (
            <tr key={s.order}>
              <td className="nowrap">{s.order}</td>
              <td className="nowrap">
                {s.title} {s.forced && <Badge tone="warn">예산 초과로 추가</Badge>}
              </td>
              <td>
                {s.missing ? (
                  "—"
                ) : s.kind === "finance" ? (
                  <>재무 담당({s.poolSize}명)</>
                ) : (
                  <>
                    {s.approverName} {s.acting && <Badge tone="info">직무대행</Badge>}
                    {s.absentUntil && <AbsenceNote until={s.absentUntil} delegate={s.absenceDelegate} />}
                  </>
                )}
              </td>
              <td>{s.unitPath || s.unitName || (s.kind === "finance" ? "—" : "")}</td>
              <td>{[s.raisedNote, s.layerNote].filter(Boolean).join(" ")}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </TableWrap>
  );
}
