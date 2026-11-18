// 지급·자금(B32) 화면의 서버 응답 형태와 한국어 표시 이름.
export type PaymentStatus = "draft" | "requested" | "approved" | "paid" | "canceled" | "paid_canceled";

export interface PaymentItem {
  payableId: number;
  journalNo: string;
  tradeDate: string;
  dueDate: string;
  amount: number;
}

export interface Payment {
  id: number;
  no: string;
  vendorId: number;
  vendorName: string;
  status: PaymentStatus;
  statusLabel: string;
  total: number;
  dueDate: string;
  bankAccountId: number;
  bankAlias: string;
  payeeAccount: string;
  memo: string;
  docId: number;
  docNo: string;
  createdBy: string;
  createdById: number;
  createdAt: string;
  approvedAt?: string;
  cancelReason: string;
  reverseReason: string;
  paidDate: string;
  transferRef: string;
  paidBy: string;
  journalNo: string;
  reversalNo: string;
  warnings: string[];
  accountDiffers: boolean;
  items?: PaymentItem[];
}

export interface Candidate {
  payableId: number;
  journalNo: string;
  tradeDate: string;
  dueDate: string;
  amount: number;
  paid: number;
  reserved: number;
  available: number;
  overdueDays: number;
  openRequests: number;
}

export interface BankAccount {
  id: number;
  alias: string;
  bankName: string;
  accountNo: string;
  ledgerCode: string;
  ledgerName: string;
  openingBalance: number;
  openingDate: string;
  active: boolean;
  balance: number;
}

export interface CalendarEntry {
  date: string;
  kind: "due" | "payment" | "requested";
  vendorName: string;
  amount: number;
  paymentNo: string;
  status: string;
  statusLabel: string;
  delayed: boolean;
  nextBusinessDay?: string;
}

export interface Calendar {
  from: string;
  to: string;
  entries: CalendarEntry[];
  overdueTotal: number;
  scheduledTotal: number;
  balanceTotal: number;
  shortageWarn: boolean;
}

export interface FundDaily {
  from: string;
  to: string;
  opening: number;
  rows: { date: string; bankAccountId: number; bankAlias: string; opening: number; deposit: number; withdraw: number; closing: number; pendingDeposit: number }[];
  movements: { date: string; bankAlias: string; kind: string; kindLabel: string; amount: number; paymentNo: string; vendorName: string; memo: string; journalNo: string; pending: boolean }[];
  totalDeposit: number;
  totalWithdraw: number;
  closing: number;
  ledgerBalance: number;
  matches: boolean;
  pendingCount: number;
}

export const PAYMENT_STATUS_TONE: Record<PaymentStatus, "muted" | "info" | "ok" | "warn" | "bad"> = {
  draft: "muted",
  requested: "info",
  approved: "warn",
  paid: "ok",
  canceled: "bad",
  paid_canceled: "bad",
};

export const PAYMENT_STATUS_CHOICES: [string, string][] = [
  ["", "전체"],
  ["draft", "임시"],
  ["requested", "승인 요청"],
  ["approved", "승인(지급 대기)"],
  ["paid", "지급 완료"],
  ["canceled", "취소"],
  ["paid_canceled", "지급 취소"],
];
