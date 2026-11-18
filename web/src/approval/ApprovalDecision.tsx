// 결재 화면: 내용 확인 후 승인·반려. 반려는 사유가 있어야 누를 수 있다.
import { useMemo, useState } from "react";
import { useParams } from "react-router-dom";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { PermissionGate } from "../auth/PermissionGate";
import { useAside } from "../layout/aside";
import { Breadcrumbs, Notice, useToast } from "../layout/ui";
import { ApprovalInfoPanel } from "../purchase/asidePanels";
import { PurchaseView } from "../purchase/PurchaseView";
import { OrderNotice } from "../purchase/OrderNotice";
import { QuoteCard } from "../purchase/QuoteCard";
import { ActingNotice } from "./ActingNotice";
import { ReassignDialog, type ReassignTarget } from "./ReassignDialog";
import { PR_STATUS_LABEL, label } from "../purchase/labels";
import type { ApprovalStep, DecisionResult, HistoryRecord, PurchaseRequest } from "../purchase/types";
import { errorText, useLoad } from "../purchase/useLoad";

export function ApprovalDecision() {
  const { id } = useParams();
  const pr = useLoad(() => api.get<PurchaseRequest>(`/api/purchase-requests/${id}`), [id]);
  const hist = useLoad(() => api.get<HistoryRecord[]>(`/api/approvals/${id}/history`), [id]);
  const [comment, setComment] = useState("");
  const [error, setError] = useState("");
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const { can, user } = useAuth();
  const [target, setTarget] = useState<ReassignTarget | null>(null);

  async function decide(approve: boolean) {
    setBusy(true);
    setError("");
    try {
      const res = await api.post<DecisionResult>(`/api/approvals/${id}/decision`, { approve, comment: comment.trim() });
      toast(
        `결재했습니다. 현재 상태는 ${label(PR_STATUS_LABEL, res.request.status)}입니다.` +
          (res.purchaseOrder ? ` 발주 ${res.purchaseOrder.no} 이(가) 생성되었습니다.` : ""),
      );
      setComment("");
      await pr.reload();
      await hist.reload();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

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
  const waiting = pr.data.steps.some((s) => s.status === "pending");

  return (
    <section>
      <Breadcrumbs items={[{ label: "결재함", to: "/approvals" }, { label: pr.data.no }]} />
      <PermissionGate permission="approval:decide">
        <ActingNotice prId={pr.data.id} myName={user?.name ?? ""} version={`${pr.data.status}-${pr.data.updatedAt}`} />
      </PermissionGate>
      <PurchaseView
        pr={pr.data}
        history={hist.data ?? []}
        hideApproval
      />
      {(!pr.data.formKind || pr.data.formKind === "purchase") && <QuoteCard prId={pr.data.id} editable={pr.data.editable} />}
      {pr.data.purchaseOrderId > 0 && <OrderNotice prId={pr.data.id} purchaseOrderId={pr.data.purchaseOrderId} />}
      {target && (
        <ReassignDialog
          target={target}
          onClose={() => setTarget(null)}
          onDone={(m) => {
            setTarget(null);
            toast(m);
            void pr.reload();
            void hist.reload();
          }}
        />
      )}
      <PermissionGate permission="approval:decide">
        {waiting ? (
          <div className="card">
            <h3>결재</h3>
            <div className="field">
              <label htmlFor="decision-comment">의견 (반려할 때는 사유가 필요합니다)</label>
              <textarea id="decision-comment" value={comment} onChange={(e) => setComment(e.target.value)} rows={3} />
            </div>
            {error && <Notice tone="error">{error}</Notice>}
            <div className="form-row">
              <button type="button" disabled={busy} onClick={() => void decide(true)}>
                승인
              </button>
              <button type="button" className="btn-danger" disabled={busy || comment.trim() === ""} onClick={() => void decide(false)}>
                반려
              </button>
            </div>
          </div>
        ) : (
          <p>결재할 수 있는 단계가 없습니다.</p>
        )}
      </PermissionGate>
    </section>
  );
}
