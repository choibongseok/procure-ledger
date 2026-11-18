// 구매요청 상세: 내용·결재선·이력, 요청자는 수정·상신·재상신.
import { useMemo, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { NudgePanel } from "../approval/NudgePanel";
import { useAside } from "../layout/aside";
import { Breadcrumbs, Notice } from "../layout/ui";
import { ApprovalInfoPanel } from "./asidePanels";
import { ReassignDialog, type ReassignTarget } from "../approval/ReassignDialog";
import { CancelSubmit } from "../document/CancelSubmit";
import { LinePreviewCard, type LineReady } from "./LinePreviewCard";
import { OrderNotice } from "./OrderNotice";
import { PurchaseView } from "./PurchaseView";
import { QuoteCard } from "./QuoteCard";
import type { ApprovalStep, HistoryRecord, PurchaseRequest } from "./types";
import { errorText, useLoad } from "./useLoad";

export function PurchaseDetail() {
  const { id } = useParams();
  const { user, can } = useAuth();
  const [actionError, setActionError] = useState("");
  const [lineReady, setLineReady] = useState<LineReady>(null);
  const [target, setTarget] = useState<ReassignTarget | null>(null);
  const [done, setDone] = useState("");
  const pr = useLoad(() => api.get<PurchaseRequest>(`/api/purchase-requests/${id}`), [id]);
  const hist = useLoad(
    () => (can("approval:read") ? api.get<HistoryRecord[]>(`/api/approvals/${id}/history`) : Promise.resolve([])),
    [id],
  );

  const reassign =
    pr.data && can("approval:reassign") && (pr.data.status === "submitted" || pr.data.status === "in_review")
      ? (st: ApprovalStep) =>
          setTarget({
            prId: pr.data!.id,
            seq: st.seq,
            title: st.title || "결재자",
            currentName: st.assigneeName ?? "",
            currentUnit: st.unitPath,
            stalled: !!pr.data!.noApprover && st.status === "pending",
          })
      : undefined;
  const asideContent = useMemo(
    () => (pr.data ? <ApprovalInfoPanel pr={pr.data} history={hist.data ?? []} onReassign={reassign} /> : null),
    [pr.data, hist.data, !!reassign], // eslint-disable-line react-hooks/exhaustive-deps
  );
  useAside(asideContent ? { title: "결재 정보", content: asideContent } : null);

  if (pr.error) return <Notice tone="error">{pr.error}</Notice>;
  if (!pr.data) return <p>불러오는 중…</p>;
  const p = pr.data;
  const mine = user?.id === p.requesterId && can("purchase:create");
  const editable = mine && !!p.editable;

  async function submit() {
    setActionError("");
    try {
      await api.post(`/api/purchase-requests/${id}/submit`);
      await pr.reload();
      await hist.reload();
    } catch (e) {
      setActionError(errorText(e));
    }
  }

  async function withdraw() {
    setActionError("");
    try {
      await api.post(`/api/purchase-requests/${id}/withdraw`);
      await pr.reload();
      await hist.reload();
    } catch (e) {
      setActionError(errorText(e));
    }
  }

  return (
    <section>
      <Breadcrumbs items={[{ label: "구매요청", to: "/purchase-requests" }, { label: p.no }]} />
      {done && <Notice tone="ok">{done}</Notice>}
      <p>
        <Link to={`/purchase-requests/${p.id}/print`}>출력 화면</Link>
      </p>
      <PurchaseView
        pr={p}
        history={hist.data ?? []}
        hideApproval
      />
      {target && (
        <ReassignDialog
          target={target}
          onClose={() => setTarget(null)}
          onDone={(m) => {
            setTarget(null);
            setDone(m);
            void pr.reload();
            void hist.reload();
          }}
        />
      )}
      {(!p.formKind || p.formKind === "purchase") && <QuoteCard key={`${p.id}-${p.status}`} prId={p.id} editable={p.editable} />}
      {p.purchaseOrderId > 0 && <OrderNotice prId={p.id} purchaseOrderId={p.purchaseOrderId} />}
      {actionError && <Notice tone="error">{actionError}</Notice>}
      {user?.id === p.requesterId && (p.status === "submitted" || p.status === "in_review") && <NudgePanel key={`${p.id}-${p.status}`} docId={p.id} />}
      {user?.id === p.requesterId && p.canCancelSubmit && (
        <CancelSubmit
          path={`/api/purchase-requests/${id}/cancel-submit`}
          onDone={() => {
            setDone("상신을 취소했습니다. 임시저장 상태에서 고쳐 다시 상신할 수 있습니다.");
            void pr.reload();
            void hist.reload();
          }}
        />
      )}
      {p.noApprover && (
        <Notice tone="warn">
          결재자 없음: 지금 차례 단계를 결재할 수 있는 사람이 없어 결재가 멈춰 있습니다. 결재자가 퇴사했거나 결재 권한을 잃었을 수 있습니다.
          {mine && (
            <>
              {" "}
              <button type="button" className="btn-danger btn-sm" onClick={() => void withdraw()}>
                회수(임시저장으로 되돌리기)
              </button>
            </>
          )}
        </Notice>
      )}
      {editable && (
        <>
          <LinePreviewCard amount={p.totalAmount} onReady={setLineReady} />
          <div className="form-row">
          <Link className="btn btn-secondary" to={`/purchase-requests/${p.id}/edit`}>
            내용 수정
          </Link>
          <button type="button" disabled={lineReady === false} onClick={() => void submit()}>
            {p.status === "rejected" ? "재상신" : "상신"}
          </button>
          </div>
        </>
      )}
    </section>
  );
}
