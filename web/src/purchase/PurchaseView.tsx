// 구매요청 상세 본문(내용·결재선·결재 이력). 구매요청 상세와 결재 화면이 같이 쓴다.
import { Badge, EmptyState, Notice, PageHead, TableWrap, type Tone } from "../layout/ui";
import { PrStatusBadge } from "./badges";
import { BudgetOverBadge, monthText } from "./BudgetCheckCard";
import type { ApprovalStep, HistoryRecord, PurchaseRequest } from "./types";
import { HISTORY_ACTION_LABEL, STEP_KIND_LABEL, STEP_STATUS_LABEL, dateTime, label, won } from "./labels";

/** 처리자 칸: 대결(위임·부재)은 "○○ (대결: 원결재자 △△)" 로 둘의 이름을 함께 보인다(B29 3.4, 위임·부재 모두 같은 문구). */
export function actorText(h: HistoryRecord): string {
  const name = h.actorName || "알 수 없음";
  if (!h.onBehalfOfId) return name;
  return `${name} (대결: 원결재자 ${h.onBehalfOfName || "알 수 없음"})`;
}

const STEP_TONE: Record<string, Tone> = { waiting: "muted", pending: "warn", approved: "ok", rejected: "bad" };

/** 결재자 칸: 현재 결재자(재지정 반영). 재무 단계는 역할 풀이라 사람 이름이 없다. */
function approverText(s: ApprovalStep) {
  if (s.kind === "finance") return <>재무 담당</>;
  if (!s.assigneeName) return <>—</>;
  return (
    <>
      {s.assigneeName} {s.acting && <Badge tone="info">직무대행</Badge>}
      {s.reassignedFrom && <span className="hint"> (결재자 변경: {s.reassignedFrom} → {s.assigneeName})</span>}
    </>
  );
}

/** 상신 당시 조직의 기준일(YYYY-MM-DD)을 "2026. 11. 11." 로 보인다. */
function lineDay(d: string): string {
  const t = new Date(`${d}T00:00:00`);
  return Number.isNaN(t.getTime()) ? d : t.toLocaleDateString("ko-KR");
}

export function PurchaseView({
  pr,
  history,
  onReassign,
  hideApproval,
}: {
  pr: PurchaseRequest;
  history: HistoryRecord[];
  /** 주면 아직 끝나지 않은 직책 단계 행에 "결재자 변경" 버튼을 보인다(admin 전용, 부모가 권한을 판단한다). */
  onReassign?: (step: ApprovalStep) => void;
  /** true 면 결재선·참조·이력을 본문에서 빼고(우측 패널 "결재 정보"가 그린다) 내용만 보인다. */
  hideApproval?: boolean;
}) {
  const open = (s: ApprovalStep) => s.status === "pending" || s.status === "waiting";
  return (
    <div>
      <PageHead title={<>{pr.title} <small>({pr.no})</small></>} />
      <dl>
        <dt>상태</dt>
        <dd>
          <PrStatusBadge status={pr.status} /> <BudgetOverBadge budget={pr.budget} />
        </dd>
        <dt>요청자</dt>
        <dd>{pr.requesterName}</dd>
        <dt>합계 금액</dt>
        <dd>{won(pr.totalAmount)}</dd>
        {pr.formKind === "purchase" || !pr.formKind ? (
          <>
            <dt>희망 납기</dt>
            <dd>{pr.dueDate ? pr.dueDate : "지정하지 않음"}</dd>
            <dt>납품처</dt>
            <dd>{pr.deliveryPlace || "본사 창고"}</dd>
          </>
        ) : null}
        {pr.budget && pr.budget.state !== "none" && (
          <>
            <dt>예산 확인</dt>
            <dd>
              {pr.budget.state === "unregistered"
                ? `${monthText(pr.budget.month)} 예산이 등록되지 않아 확인하지 않았습니다.`
                : pr.budget.state === "over"
                  ? `${monthText(pr.budget.month)} 예산 ${won(pr.budget.budget)} 중 이 요청이 ${won(pr.budget.over)} 초과했습니다. ${pr.budget.financeAdded === true ? "예산 초과로 재무 단계가 더해졌습니다" : pr.budget.financeAdded === false ? "예산 초과 요청이지만 재무 단계는 이미 포함되어 있습니다" : "예산 초과 요청입니다"}. (상신 당시 기준)`
                  : `${monthText(pr.budget.month)} 예산 ${won(pr.budget.budget)} 이내입니다. (상신 당시 기준)`}
            </dd>
          </>
        )}
        {pr.reason && (
          <>
            <dt>사유</dt>
            <dd>{pr.reason}</dd>
          </>
        )}
        {pr.status === "rejected" && pr.rejectReason && (
          <>
            <dt>반려 사유</dt>
            <dd role="note">{pr.rejectReason}</dd>
          </>
        )}
      </dl>

      <h3>품목</h3>
      <TableWrap>
        <table>
          <thead>
            <tr>
              <th>품목</th>
              <th className="num nowrap">수량</th>
              <th className="num nowrap">단가</th>
              <th className="num nowrap">금액</th>
            </tr>
          </thead>
          <tbody>
            {pr.lines.map((l) => (
              <tr key={l.id}>
                <td>{l.itemName}</td>
                <td className="num nowrap">{l.quantity.toLocaleString("ko-KR")}</td>
                <td className="num nowrap">{won(l.unitPrice)}</td>
                <td className="num nowrap">{won(l.amount)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>

      {!hideApproval && (
        <>
      <h3>결재선</h3>
      {pr.steps.length > 0 &&
        (pr.lineAsOf ? (
          <p className="hint">
            상신 당시 조직: {pr.requesterUnitPath || "—"} (기준일 {lineDay(pr.lineAsOf)})
          </p>
        ) : (
          <p className="hint">상신 당시 조직 기록이 없습니다(조직도 도입 이전 건).</p>
        ))}
      {pr.steps
        .filter((s) => open(s) && s.headChangedTo)
        .map((s) => (
          <Notice key={s.seq} tone="info">
            {s.headChangedTo === "공석"
              ? `상신 당시 ${s.title ?? label(STEP_KIND_LABEL, s.kind)}은 ${s.assigneeName}님이었고 지금은 공석입니다.`
              : `상신 당시 ${s.title ?? label(STEP_KIND_LABEL, s.kind)}은 ${s.assigneeName}님이었고 지금은 ${s.headChangedTo}님입니다.`}{" "}
            결재는 상신 당시 결재자가 하며, 필요하면 결재자가 위임할 수 있습니다.
          </Notice>
        ))}
      {pr.steps.length === 0 ? (
        <EmptyState>아직 상신하지 않아 결재선이 없습니다.</EmptyState>
      ) : (
        <TableWrap>
          <table>
            <thead>
              <tr>
                <th className="nowrap">단계</th>
                <th className="nowrap">직책</th>
                <th>결재자</th>
                <th>소속 조직</th>
                <th className="nowrap">상태</th>
                <th className="nowrap">처리 시각</th>
                <th>의견</th>
                {onReassign && <th />}
              </tr>
            </thead>
            <tbody>
              {pr.steps.map((s) => (
                <tr key={s.seq}>
                  <td className="nowrap">{s.seq}</td>
                  <td className="nowrap">{s.title || label(STEP_KIND_LABEL, s.kind)}</td>
                  <td style={{ minWidth: "9em" }}>{approverText(s)}</td>
                  <td style={{ minWidth: "12em" }}>{s.unitPath || "—"}</td>
                  <td className="nowrap">
                    <Badge tone={STEP_TONE[s.status] ?? "muted"}>{label(STEP_STATUS_LABEL, s.status)}</Badge>
                  </td>
                  <td className="nowrap">{s.decidedAt ? dateTime(s.decidedAt) : ""}</td>
                  <td>{s.comment}</td>
                  {onReassign && (
                    <td className="nowrap">
                      {open(s) && s.kind !== "finance" && (
                        <button type="button" className="btn-secondary btn-sm" onClick={() => onReassign(s)}>
                          결재자 변경
                        </button>
                      )}
                      {open(s) && s.kind === "finance" && <span className="hint">재무 단계는 재무 담당 역할을 가진 사람이 맡습니다.</span>}
                      {!open(s) && <span className="hint">끝난 단계</span>}
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}

      {(pr.recipients ?? []).filter((r) => !(r.kind === "cc" && r.targetType === "user" && r.note)).length > 0 && (
        <ul>
          {(pr.recipients ?? [])
            .filter((r) => !(r.kind === "cc" && r.targetType === "user" && r.note))
            .map((r) => (
              <li key={`${r.kind}-${r.targetType}-${r.targetId}`}>
                {r.kind === "cc" ? "참조" : "수신"}: {r.name}
                {r.path ? ` (${r.path})` : ""}
              </li>
            ))}
        </ul>
      )}

      <h3>결재 이력</h3>
      {history.length === 0 ? (
        <EmptyState>이력이 없습니다.</EmptyState>
      ) : (
        <TableWrap>
          <table>
            <thead>
              <tr>
                <th className="nowrap">일시</th>
                <th>처리자</th>
                <th className="nowrap">동작</th>
                <th>의견</th>
              </tr>
            </thead>
            <tbody>
              {history.map((h) => (
                <tr key={h.id}>
                  <td className="nowrap">{dateTime(h.at)}</td>
                  <td>{actorText(h)}</td>
                  <td className="nowrap">{label(HISTORY_ACTION_LABEL, h.action)}</td>
                  <td>{h.comment}</td>
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
