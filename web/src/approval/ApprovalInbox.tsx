// 결재함: 지금 내 차례인 구매요청 목록.
import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { PermissionGate } from "../auth/PermissionGate";
import { useAside } from "../layout/aside";
import { Badge, EmptyState, Notice, PageHead, TableWrap } from "../layout/ui";
import { InboxSummaryPanel } from "../purchase/asidePanels";
import { PrStatusBadge } from "../purchase/badges";
import { BudgetOverBadge } from "../purchase/BudgetCheckCard";
import { STEP_KIND_LABEL, label, won } from "../purchase/labels";
import type { DelegationList, PurchaseRequest } from "../purchase/types";
import { useLoad } from "../purchase/useLoad";

type Tab = "pending" | "progress" | "done";
const TABS: { id: Tab; name: string; intro: string; empty: string }[] = [
  { id: "pending", name: "대기", intro: "지금 내 차례인 결재 건입니다.", empty: "결재할 건이 없습니다." },
  { id: "progress", name: "진행", intro: "내가 결재선에 있고 아직 끝나지 않은 건입니다. 내 차례가 아닌 건도 함께 보입니다.", empty: "진행 중인 결재가 없습니다." },
  { id: "done", name: "완료", intro: "내가 결재했고 결재가 끝난 건입니다.", empty: "완료된 결재가 없습니다." },
];

/** 내가 지금 부재 중이면 결재함 위쪽에 안내한다: 대결자와 종료 시각, 대기 건은 대결자도 처리할 수 있고 나도 직접 결재할 수 있다(B29 3.4). 결재 위임 권한이 있는 사람에게만 그려진다. */
function AbsenceBanner() {
  const list = useLoad(() => api.get<DelegationList>("/api/approvals/delegations"), []);
  const now = Date.now();
  const mine = (list.data?.given ?? []).find((d) => d.kind === "absence" && !d.revokedAt && new Date(d.from).getTime() <= now && now < new Date(d.to).getTime());
  if (!mine) return null;
  const end = new Date(new Date(mine.to).getTime() + 9 * 3600 * 1000);
  return (
    <Notice tone="warn">
      <Badge tone="warn">부재 중</Badge> ~{end.getUTCMonth() + 1}. {end.getUTCDate()}. · 대결자 {mine.delegateName}. 대기 중인 결재는 대결자도 처리할 수 있고, 돌아와서 직접 결재해도 됩니다.
    </Notice>
  );
}

export function ApprovalInbox() {
  const [tab, setTab] = useState<Tab>("pending");
  const cur = TABS.find((t) => t.id === tab)!;
  const { data, error, loading } = useLoad(
    () => api.get<PurchaseRequest[]>(tab === "pending" ? "/api/approvals/pending" : `/api/approvals/inbox?tab=${tab}`),
    [tab],
  );
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const selected = data?.find((pr) => pr.id === selectedId) ?? null;
  const content = useMemo(() => (selected ? <InboxSummaryPanel pr={selected} /> : null), [selected]);
  useAside(content ? { title: "선택한 건", content } : null);
  return (
    <section>
      <PageHead title="결재함" />
      <PermissionGate permission="approval:delegate">
        <AbsenceBanner />
      </PermissionGate>
      <div className="form-row" role="tablist" aria-label="결재함 구분">
        {TABS.map((t) => (
          <button key={t.id} type="button" role="tab" aria-selected={tab === t.id} className={tab === t.id ? "" : "btn-secondary"} onClick={() => {
              setTab(t.id);
              setSelectedId(null);
            }}>
            {t.name}
          </button>
        ))}
      </div>
      <p>{cur.intro}</p>
      {error && <Notice tone="error">{error}</Notice>}
      {loading && <p>불러오는 중…</p>}
      {data && data.length === 0 && <EmptyState>{cur.empty}</EmptyState>}
      {data && data.length > 0 && (
        <TableWrap>
          <table>
            <thead>
              <tr>
                <th className="nowrap">선택</th>
                <th className="nowrap">번호</th>
                <th>제목</th>
                <th className="nowrap">요청자</th>
                <th className="num nowrap">금액</th>
                <th>{tab === "pending" ? "내 단계" : "상태"}</th>
              </tr>
            </thead>
            <tbody>
              {data.map((pr) => {
                const mine = pr.steps.find((s) => s.status === "pending");
                return (
                  <tr key={pr.id} aria-selected={pr.id === selectedId ? "true" : undefined}>
                    <td className="nowrap">
                      <input
                        type="radio"
                        name="inbox-select"
                        aria-label={`${pr.no} 선택`}
                        checked={pr.id === selectedId}
                        onChange={() => setSelectedId(pr.id)}
                      />
                    </td>
                    <td className="nowrap">{pr.no}</td>
                    <td>
                      <Link to={pr.formKind && pr.formKind !== "purchase" ? `/docs/${pr.id}` : `/approvals/${pr.id}`}>{pr.title}</Link>{" "}
                      <BudgetOverBadge budget={pr.budget} />
                    </td>
                    <td className="nowrap">{pr.requesterName}</td>
                    <td className="num nowrap">{won(pr.totalAmount)}</td>
                    <td>
                      {tab !== "pending" ? <PrStatusBadge status={pr.status} /> : null}
                      {tab === "pending" && mine ? mine.title || label(STEP_KIND_LABEL, mine.kind) : ""}
                      {pr.delegatedFromName && (
                        <>
                          {" "}
                          <Badge tone="info">대결</Badge> 원결재자 {pr.delegatedFromName}
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
      <PermissionGate permission="approval:delegate">
        <p>
          자리를 비울 때는 <Link to="/approvals/absence">부재·대결 설정</Link>에서 기간과 대결자를 정해 두면 결재가 멈추지 않습니다.
        </p>
      </PermissionGate>
    </section>
  );
}
