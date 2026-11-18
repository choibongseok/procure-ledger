// 결재 화면 맨 위의 위임 결재 안내 상자. 위임으로 대신 결재하는 경우에만 그린다.
import { api } from "../api/client";
import { Notice } from "../layout/ui";
import { actorText } from "../purchase/PurchaseView";
import { useLoad } from "../purchase/useLoad";

export interface ActingInfo {
  delegatorId: number;
  delegatorName: string;
  from: string;
  to: string;
}

/** "2026. 11. 10. 오전 9:00" 모양(초 없음). 위임 기간은 분 단위로 지정한다. */
export function periodTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleString("ko-KR", { year: "numeric", month: "numeric", day: "numeric", hour: "numeric", minute: "2-digit" });
}

/** 안내 문구. 이력에 남을 문구는 결재 이력 표의 처리자 칸(actorText)과 같은 함수로 만든다. */
export function actingNoticeText(info: ActingInfo, myName: string): string {
  const history = actorText({ actorName: myName, onBehalfOfId: info.delegatorId, onBehalfOfName: info.delegatorName } as Parameters<typeof actorText>[0]);
  return `위임 결재: ${info.delegatorName} 님을 대신해 결재합니다. 위임 기간 ${periodTime(info.from)} ~ ${periodTime(info.to)}. 결재 이력에는 '${history}'으로 남습니다.`;
}

/** 위임으로 결재하는 건일 때만 상자를 그린다(아니면 아무것도 그리지 않는다). 조회에 실패해도 결재 화면은 그대로 쓸 수 있다. */
export function ActingNotice({ prId, myName, version }: { prId: number; myName: string; version: string }) {
  const acting = useLoad(async () => {
    try {
      return await api.get<ActingInfo | null>(`/api/approvals/${prId}/acting`);
    } catch {
      return null;
    }
  }, [prId, version]);
  if (!acting.data?.delegatorName) return null;
  return <Notice tone="info">{actingNoticeText(acting.data, myName)}</Notice>;
}
