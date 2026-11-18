// 화면에는 영문 상태 코드를 그대로 보이지 않고 업무 용어로 바꿔 보인다.
import type { HistoryAction, POStatus, PRStatus, StepKind, StepStatus } from "./types";

export const PR_STATUS_LABEL: Record<PRStatus, string> = {
  draft: "임시저장",
  submitted: "상신",
  in_review: "결재중",
  rejected: "반려",
  approved: "승인",
  ordered: "발주완료",
};

export const STEP_KIND_LABEL: Record<StepKind, string> = {
  team_lead: "팀장",
  dept_head: "부서장",
  division_head: "본부장",
  ceo: "대표",
  finance: "재무",
};

export const STEP_STATUS_LABEL: Record<StepStatus, string> = {
  waiting: "대기",
  pending: "결재 차례",
  approved: "승인",
  rejected: "반려",
  agreed: "합의 동의",
  objected: "합의 이견",
  skipped: "건너뜀",
};

export const HISTORY_ACTION_LABEL: Record<HistoryAction, string> = {
  submit: "상신",
  resubmit: "재상신",
  approve: "승인",
  reject: "반려",
  withdraw: "회수",
  reassign: "결재자 변경",
  agree: "합의 동의",
  object: "합의 이견",
  cancel_submit: "상신 취소",
};

export const PO_STATUS_LABEL: Record<POStatus, string> = {
  open: "입고 전",
  partial: "부분 입고",
  received: "입고 완료",
};

/** 모르는 코드는 영문 그대로 보이지 않고 "알 수 없음"으로 보인다. */
export function label<K extends string>(map: Record<K, string>, key: string): string {
  return (map as Record<string, string>)[key] ?? "알 수 없음";
}

export function won(n: number): string {
  return `${n.toLocaleString("ko-KR")}원`;
}

export function dateTime(iso: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleString("ko-KR");
}
