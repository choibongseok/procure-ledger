// 구매·발주 상태를 공통 배지로 그린다. 글자는 업무 용어(labels.ts), 색은 뜻에 따라 정한다.
import { Badge, type Tone } from "../layout/ui";
import { PO_STATUS_LABEL, PR_STATUS_LABEL, label } from "./labels";

const PR_TONE: Record<string, Tone> = {
  draft: "muted",
  submitted: "info",
  in_review: "info",
  rejected: "bad",
  approved: "ok",
  ordered: "ok",
};

const PO_TONE: Record<string, Tone> = { open: "warn", partial: "warn", received: "ok" };

export function PrStatusBadge({ status }: { status: string }) {
  return <Badge tone={PR_TONE[status] ?? "muted"}>{label(PR_STATUS_LABEL, status)}</Badge>;
}

export function PoStatusBadge({ status }: { status: string }) {
  return <Badge tone={PO_TONE[status] ?? "muted"}>{label(PO_STATUS_LABEL, status)}</Badge>;
}
