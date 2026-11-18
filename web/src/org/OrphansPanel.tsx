// (web/src/org). 결재·합의자가 사라진 진행 중 건 목록(B22 7.2·8절). 점검 탭 맨 위에 보인다.
// 데이터는 erp 의 `GET /api/approvals/orphaned`(org:manage)이고, 행의 "결재자 변경" 은 B21 의 재지정 대화상자를 연다.
import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { ReassignDialog, type ReassignTarget } from "../approval/ReassignDialog";
import { useAuth } from "../auth/AuthProvider";
import { Badge, EmptyState, Notice, TableWrap } from "../layout/ui";
import { dayText } from "./types";

export interface Orphan {
  prId: number;
  requesterId: number;
  requesterName?: string;
  prTitle: string;
  round: number;
  order: number;
  seq: number;
  title: string;
  stepKind: string;
  approverId: number;
  approverName: string;
  reason: string;
  sinceDay: string;
  reassignable: boolean;
  role: "approve" | "agree" | string;
  formKind: "purchase" | "general" | string;
  formCode?: string;
  formName?: string;
  stage?: number;
}

/** 사라진 날이 오래된 순, 같은 날은 문서 번호 순(명세 7.3 의 10). */
export function sortOrphans(list: Orphan[]): Orphan[] {
  return [...list].sort((a, b) => (a.sinceDay === b.sinceDay ? a.prId - b.prId || a.order - b.order : a.sinceDay < b.sinceDay ? -1 : 1));
}

function docLink(o: Orphan): string {
  return o.formKind === "purchase" ? `/purchase-requests/${o.prId}` : `/docs/${o.prId}`;
}

export function OrphansPanel({ onCount }: { onCount?: (n: number) => void }) {
  const { can } = useAuth();
  const [rows, setRows] = useState<Orphan[] | null>(null);
  const [error, setError] = useState("");
  const [target, setTarget] = useState<ReassignTarget | null>(null);
  const [done, setDone] = useState("");

  const load = useCallback(() => {
    setError("");
    api
      .get<Orphan[]>("/api/approvals/orphaned")
      .then((r) => {
        const sorted = sortOrphans(Array.isArray(r) ? r : []);
        setRows(sorted);
        onCount?.(sorted.length);
      })
      .catch(() => setError("목록을 불러오지 못했습니다. 잠시 후 다시 시도하세요."));
  }, [onCount]);
  useEffect(() => {
    load();
  }, [load]);

  // 같은 문서·차수·단계 번호의 합의자가 둘 이상 사라졌으면 병렬 합의로 보인다.
  const parallel = new Set<string>();
  const seen = new Map<string, number>();
  for (const o of rows ?? []) {
    if (o.role !== "agree") continue;
    const k = `${o.prId}:${o.round}:${o.stage ?? o.order}`;
    seen.set(k, (seen.get(k) ?? 0) + 1);
    if ((seen.get(k) ?? 0) > 1) parallel.add(k);
  }

  return (
    <section aria-label="결재·합의자 소멸">
      <h3>결재·합의자가 사라진 진행 중 건 {rows && <span className="hint">{rows.length}건</span>}</h3>
      <p>
        퇴사·휴직하거나 결재·합의 권한을 잃어 처리할 사람이 사라진 진행 중 결재 문서입니다. 결재자를 다시 지정하면 결재가 이어집니다(휴직은 원인을 "장기 부재"로 고르세요). 기안자가
        문서를 회수한 뒤 다시 상신해도 현재 조직 기준으로 결재선이 새로 만들어집니다.
      </p>
      {error && <Notice tone="error">{error}</Notice>}
      {done && <Notice tone="ok">{done}</Notice>}
      {rows && rows.length === 0 && <EmptyState>결재·합의자가 사라진 진행 중 건이 없습니다.</EmptyState>}
      {rows && rows.length > 0 && (
        <TableWrap>
          <table className="stack-table">
            <thead>
              <tr>
                <th>문서 제목</th>
                <th>양식</th>
                <th>기안자</th>
                <th>단계 역할</th>
                <th>결재 단계</th>
                <th>사라진 처리자</th>
                <th>사라진 날</th>
                <th>결재 차수·순서</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {rows.map((o) => {
                const isAgree = o.role === "agree";
                const par = isAgree && parallel.has(`${o.prId}:${o.round}:${o.stage ?? o.order}`);
                return (
                  <tr key={`${o.prId}-${o.round}-${o.seq}-${o.approverId}`}>
                    <td data-label="문서 제목">
                      <Link to={docLink(o)}>{o.prTitle}</Link>
                    </td>
                    <td data-label="양식" className="nowrap">
                      {o.formName || (o.formKind === "purchase" ? "구매 품의서" : "양식 문서")}
                    </td>
                    <td data-label="기안자" className="nowrap">
                      {o.requesterName || `사용자 #${o.requesterId}`}
                    </td>
                    <td data-label="단계 역할" className="nowrap">
                      {isAgree ? (par ? "합의(병렬)" : "합의") : "결재"}
                    </td>
                    <td data-label="결재 단계" className="nowrap">
                      {isAgree ? "합의" : o.title}
                    </td>
                    <td data-label="사라진 처리자">
                      {o.approverName} <Badge tone="warn">{o.reason}</Badge>
                    </td>
                    <td data-label="사라진 날" className="nowrap">
                      {dayText(o.sinceDay)}
                    </td>
                    <td data-label="결재 차수·순서" className="nowrap">
                      {o.round}차 · {o.order}단계
                    </td>
                    <td className="nowrap">
                      {!o.reassignable ? (
                        <span className="hint">재무 단계는 재무 담당 역할을 가진 사람이 맡습니다.</span>
                      ) : (
                        can("approval:reassign") && (
                          <button
                            type="button"
                            className="btn-secondary btn-sm"
                            onClick={() => {
                              setDone("");
                              setTarget({ prId: o.prId, seq: o.seq, title: o.title, currentName: o.approverName, stalled: true });
                            }}
                          >
                            결재자 변경
                          </button>
                        )
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </TableWrap>
      )}
      {target && (
        <ReassignDialog
          target={target}
          onClose={() => setTarget(null)}
          onDone={(m) => {
            setTarget(null);
            setDone(m);
            load();
          }}
        />
      )}
    </section>
  );
}
