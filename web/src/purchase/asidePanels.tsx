// 우측 패널(useAside)에 올리는 내용: 선택한 요청·선택한 건 요약, 결재 정보(결재선·이력·첨부).
// 목록 화면(구매요청·결재함)과 상세 화면(구매요청 상세·결재 화면)이 같이 쓴다.
import { Fragment, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { Badge, Notice, type Tone } from "../layout/ui";
import { PrStatusBadge } from "./badges";
import { monthText } from "./BudgetCheckCard";
import { HISTORY_ACTION_LABEL, STEP_KIND_LABEL, STEP_STATUS_LABEL, dateTime, label, won } from "./labels";
import type { ApprovalStep, BudgetSnapshot, HistoryRecord, PurchaseRequest, QuoteCompare } from "./types";
import { useLoad } from "./useLoad";

const STEP_TONE: Record<string, Tone> = { waiting: "muted", pending: "warn", approved: "ok", rejected: "bad" };

/** 소속 문자열("본부 > 부서 > 팀")에서 마지막 조직만. 비면 "—". */
export function lastUnit(path?: string): string {
  const parts = (path ?? "").split(/\s*[>›]\s*/).map((s) => s.trim()).filter(Boolean);
  return parts.length > 0 ? parts[parts.length - 1] : "—";
}

function stepTitle(s: ApprovalStep): string {
  return s.title || label(STEP_KIND_LABEL, s.kind);
}

/** 지금 결재 차례인 단계 이름(없으면 "—"). */
export function currentStepText(pr: PurchaseRequest): string {
  const cur = pr.steps.find((s) => s.status === "pending");
  return cur ? stepTitle(cur) : "—";
}

/** 예산 확인 한 줄(패널용 짧은 문구). */
export function budgetLine(b?: BudgetSnapshot): string {
  if (!b || b.state === "none") return "—";
  if (b.state === "unregistered") return `${monthText(b.month)} 예산 미등록`;
  if (b.state === "over") return `${monthText(b.month)} 예산 ${won(b.over)} 초과`;
  return `${monthText(b.month)} 예산 이내`;
}

function Summary({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <dl>
      {rows.map(([k, v]) => (
        <Fragment key={k}>
          <dt>{k}</dt>
          <dd>{v}</dd>
        </Fragment>
      ))}
    </dl>
  );
}

/** 구매요청 목록의 선택한 요청 패널. 소속은 목록 열에서 빠져 여기에 있다. */
export function RequestSummaryPanel({ pr }: { pr: PurchaseRequest }) {
  return (
    <>
      <section className="aside-section">
        <h3>요약</h3>
        <Summary
          rows={[
            ["번호", pr.no],
            ["제목", pr.title],
            ["요청자", pr.requesterName],
            ["소속", lastUnit(pr.requesterUnitPath)],
            ["금액", won(pr.totalAmount)],
            ["상태", <PrStatusBadge key="s" status={pr.status} />],
            ["결재 단계", currentStepText(pr)],
            ["예산 확인", budgetLine(pr.budget)],
          ]}
        />
      </section>
      <section className="aside-section">
        <h3>빠른 작업</h3>
        <ul>
          <li>
            <Link to={`/purchase-requests/${pr.id}`}>상세 열기</Link>
          </li>
          {pr.status === "rejected" && (
            <li>
              <Link to={`/purchase-requests/${pr.id}`}>재상신하러 가기</Link>
            </li>
          )}
          <li>
            <Link to={`/purchase-requests/${pr.id}/quotes`}>견적 비교</Link>
          </li>
        </ul>
      </section>
    </>
  );
}

/** 첨부 패널의 견적서 줄: 견적 카드(QuoteCard)와 같은 API 로 건수를 읽는다. 읽지 못하면(권한 밖 등) 줄을 보이지 않는다. */
function QuoteCount({ prId }: { prId: number }) {
  const cmp = useLoad(() => api.get<QuoteCompare>(`/api/purchase-requests/${prId}/quotes`), [prId]);
  const quotes = cmp.data && Array.isArray(cmp.data.quotes) ? cmp.data.quotes : null;
  if (!quotes) return null;
  if (quotes.length === 0) return <p>견적서가 없습니다.</p>;
  return (
    <p>
      견적서 {quotes.length.toLocaleString("ko-KR")}건 (<Link to={`/purchase-requests/${prId}/quotes`}>견적 비교 열기</Link>)
    </p>
  );
}

/** 결재 정보 패널의 결재선 목록(현재 단계 강조·위임 표시). */
function LineList({ steps, onReassign }: { steps: ApprovalStep[]; onReassign?: (s: ApprovalStep) => void }) {
  if (steps.length === 0) return <p className="hint">아직 상신하지 않아 결재선이 없습니다.</p>;
  return (
    <ol className="approval-line">
      {steps.map((s) => {
        const open = s.status === "pending" || s.status === "waiting";
        const who = s.kind === "finance" ? "재무 담당" : s.assigneeName || "—";
        return (
          <li key={s.seq} aria-current={s.status === "pending" ? "step" : undefined}>
            <strong>
              {s.seq}. {stepTitle(s)}
            </strong>{" "}
            <Badge tone={STEP_TONE[s.status] ?? "muted"}>{label(STEP_STATUS_LABEL, s.status)}</Badge>
            <br />
            <span className="hint">
              {who}
              {s.acting ? " (직무대행)" : ""}
              {s.unitPath ? ` · ${lastUnit(s.unitPath)}` : ""}
              {s.decidedAt ? ` · ${dateTime(s.decidedAt)}` : ""}
            </span>
            {s.reassignedFrom && <span className="hint"> (결재자 변경: {s.reassignedFrom} → {s.assigneeName})</span>}
            {onReassign && open && s.kind !== "finance" && (
              <>
                {" "}
                <button type="button" className="btn-secondary btn-sm" onClick={() => onReassign(s)}>
                  결재자 변경
                </button>
              </>
            )}
          </li>
        );
      })}
    </ol>
  );
}

function actor(h: HistoryRecord): string {
  const name = h.actorName || "알 수 없음";
  return h.onBehalfOfId ? `${name} (대결: 원결재자 ${h.onBehalfOfName || "알 수 없음"})` : name;
}

/** 결재 화면·구매요청 상세의 결재 정보 패널: 결재선, 이력(시각 순), 첨부. */
export function ApprovalInfoPanel({
  pr,
  history,
  onReassign,
}: {
  pr: PurchaseRequest;
  history: HistoryRecord[];
  onReassign?: (s: ApprovalStep) => void;
}) {
  const files = pr.attachments ?? [];
  const notices = pr.steps
    .filter((s) => (s.status === "pending" || s.status === "waiting") && s.headChangedTo)
    .map((s) =>
      s.headChangedTo === "공석"
        ? `상신 당시 ${stepTitle(s)}은 ${s.assigneeName}님이었고 지금은 공석입니다.`
        : `상신 당시 ${stepTitle(s)}은 ${s.assigneeName}님이었고 지금은 ${s.headChangedTo}님입니다.`,
    );
  const recips = (pr.recipients ?? []).filter((r) => !(r.kind === "cc" && r.targetType === "user" && r.note));
  const sorted = [...history].sort((a, b) => a.at.localeCompare(b.at));
  return (
    <>
      <section className="aside-section">
        <h3>결재선</h3>
        {pr.steps.length > 0 && (
          <p className="hint">{pr.lineAsOf ? `상신 당시 조직: ${pr.requesterUnitPath || "—"}` : "상신 당시 조직 기록이 없습니다."}</p>
        )}
        {notices.map((n) => (
          <Notice key={n} tone="info">
            {n}
          </Notice>
        ))}
        <LineList steps={pr.steps} onReassign={onReassign} />
        {recips.length > 0 && (
          <ul>
            {recips.map((r) => (
              <li key={`${r.kind}-${r.targetType}-${r.targetId}`}>
                {r.kind === "cc" ? "참조" : "수신"}: {r.name}
              </li>
            ))}
          </ul>
        )}
      </section>
      <section className="aside-section">
        <h3>이력</h3>
        {sorted.length === 0 ? (
          <p className="hint">이력이 없습니다.</p>
        ) : (
          <ol>
            {sorted.map((h) => (
              <li key={h.id}>
                <span className="hint">{dateTime(h.at)}</span>
                <br />
                <strong>
                  {actor(h)} {label(HISTORY_ACTION_LABEL, h.action)}
                </strong>
                {h.comment && (
                  <>
                    <br />
                    <span>“{h.comment}”</span>
                  </>
                )}
              </li>
            ))}
          </ol>
        )}
        <p className="hint">결재 이력은 고칠 수 없습니다. 회수·재상신도 새 줄로 남습니다.</p>
      </section>
      <section className="aside-section">
        <h3>첨부</h3>
        {files.length === 0 ? (
          <p>첨부 파일이 없습니다.</p>
        ) : (
          <>
            <p>첨부 {files.length.toLocaleString("ko-KR")}개</p>
            <ul>
              {files.map((a) => (
                <li key={a.id}>
                  <a href={`/api/approval-docs/attachments/${a.id}`}>{a.filename}</a>
                </li>
              ))}
            </ul>
          </>
        )}
        {(!pr.formKind || pr.formKind === "purchase") && <QuoteCount prId={pr.id} />}
      </section>
    </>
  );
}

/** 결재함의 선택한 건 패널: 요약(요청자·부서·금액·예산 초과 여부), 결재선(현재 단계), 열기. */
export function InboxSummaryPanel({ pr }: { pr: PurchaseRequest }) {
  const openPath = pr.formKind && pr.formKind !== "purchase" ? `/docs/${pr.id}` : `/approvals/${pr.id}`;
  const over = pr.budget?.state === "over";
  return (
    <>
      <section className="aside-section">
        <h3>요약</h3>
        <Summary
          rows={[
            ["번호", pr.no],
            ["제목", pr.title],
            ["요청자", pr.requesterName],
            ["소속", lastUnit(pr.requesterUnitPath)],
            ["금액", won(pr.totalAmount)],
            ["예산 초과 여부", over ? `초과 (${won(pr.budget!.over)})` : "초과 아님"],
          ]}
        />
      </section>
      <section className="aside-section">
        <h3>결재선</h3>
        <LineList steps={pr.steps} />
      </section>
      <section className="aside-section">
        <h3>빠른 작업</h3>
        <ul>
          <li>
            <Link to={openPath}>열기</Link>
          </li>
        </ul>
      </section>
    </>
  );
}
