// 법인카드(B32) 화면의 서버 응답 형태와 한국어 표시 이름. 카드번호 조각은 어디에도 없다(별칭만).
export type CardTxnStatus = "unreconciled" | "submitted" | "confirmed" | "needs_fix" | "not_required" | "orphan_cancel" | "refund_requested" | "refunded";

export interface Card {
  id: number;
  alias: string;
  issuer: string;
  issuerLabel: string;
  holderId: number;
  holderName: string;
  active: boolean;
}

export interface Explanation {
  id: number;
  purpose: string;
  accountCode: string;
  accountName: string;
  vatDeduct: boolean;
  vatReason: string;
  vatReasonLabel: string;
  supply: number;
  vat: number;
  personal: boolean;
  status: "submitted" | "returned" | "confirmed" | "withdrawn";
  submittedAt: string;
  confirmedBy: string;
  returnReason: string;
  attachments: { id: number; filename: string; size: number }[];
}

export interface CardTxn {
  id: number;
  cardId: number;
  cardAlias: string;
  holderId: number;
  holderName: string;
  usedAt: string;
  merchant: string;
  amount: number;
  kind: "approve" | "cancel";
  status: CardTxnStatus;
  statusLabel: string;
  dueDate: string;
  daysLeft: number;
  overdue: boolean;
  journalNo: string;
  refundNote: string;
  explanation?: Explanation;
  history?: Explanation[];
}

export interface ImportPreview {
  format: string;
  formatLabel: string;
  total: number;
  new: number;
  duplicate: number;
  errors: number;
  errorRows: { rowNo: number; reason: string }[];
  totalAmount: number;
  unregistered: string[];
  warnings: string[];
}

export interface CardOverview {
  statuses: Record<string, { count: number; amount: number; label: string }>;
  delayed: CardTxn[];
  noHolder: CardTxn[];
  needsFinance: CardTxn[];
  refundRequested: CardTxn[];
}

export const CARD_STATUS_TONE: Record<CardTxnStatus, "muted" | "info" | "ok" | "warn" | "bad"> = {
  unreconciled: "warn",
  submitted: "info",
  confirmed: "ok",
  needs_fix: "bad",
  not_required: "muted",
  orphan_cancel: "bad",
  refund_requested: "warn",
  refunded: "muted",
};

export const CARD_STATUS_CHOICES: [string, string][] = [
  ["", "전체"],
  ["unreconciled", "미소명"],
  ["needs_fix", "보완 필요"],
  ["submitted", "소명됨(확인 대기)"],
  ["confirmed", "확인됨"],
  ["refund_requested", "환입 요청"],
  ["refunded", "환입 완료"],
  ["not_required", "소명 불필요(취소됨)"],
];

export const VAT_REASONS: [string, string][] = [
  ["entertainment", "접대비"],
  ["personal_suspect", "개인 사용 의심"],
  ["other", "기타"],
];
