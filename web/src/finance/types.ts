// 재무(B31) 화면의 서버 응답 형태와 한국어 표시 이름.
export interface Account {
  id: number;
  code: string;
  name: string;
  kind: "asset" | "liability" | "equity" | "revenue" | "expense";
  kindLabel: string;
  active: boolean;
  system: boolean;
}

export interface AccountRule {
  key: string;
  label: string;
  accountCode: string;
  accountName: string;
  active: boolean;
}

export type JournalStatus = "draft" | "requested" | "approved" | "canceled";

export interface JournalLine {
  seq: number;
  accountCode: string;
  accountName: string;
  side: "D" | "C";
  amount: number;
  vendorId: number;
  vendorName: string;
  memo: string;
}

export interface Journal {
  /** 사업장·법인 간 거래 표시는 값이 있을 때만 온다(B44). */
  siteId?: number;
  siteName?: string;
  intercompany?: string;
  id: number;
  no: string;
  kind: string;
  kindLabel: string;
  status: JournalStatus;
  statusLabel: string;
  postingDate: string;
  tradeDate: string;
  memo: string;
  totalAmount: number;
  reversesNo: string;
  reversedByNo: string;
  corrected: boolean;
  docId: number;
  docNo: string;
  createdBy: string;
  createdAt: string;
  approvedBy: string;
  approvedAt?: string;
  cancelReason: string;
  lines?: JournalLine[];
  evidence?: { kind: string; refId: number }[];
}

export interface QueueItem {
  id: number;
  sourceType: string;
  sourceId: number;
  sourceLabel: string;
  reason: string;
  reasonText: string;
  poNo: string;
  vendorName: string;
  amount?: number;
  status: "pending" | "created" | "manual" | "skipped";
  statusLabel: string;
  journalNo: string;
  handledBy: string;
  handledAt?: string;
  note: string;
  createdAt: string;
}

export interface PayableVendorRow {
  vendorId: number;
  vendorName: string;
  count: number;
  outstanding: number;
  reserved: number;
  earliestDue: string;
  overdueAmount: number;
  paymentDays: number;
  /** 거래처가 그룹의 다른 법인이면 그 법인 이름(법인 간 거래 배지, B44). 아니면 빠진다. */
  intercompany?: string;
}

export interface PayableReport {
  asOf: string;
  rows: PayableVendorRow[];
  total: number;
  ledgerBalance: number;
  matches: boolean;
}

export interface PayableItem {
  id: number;
  journalNo: string;
  tradeDate: string;
  dueDate: string;
  amount: number;
  paid: number;
  reserved: number;
  outstanding: number;
  overdueDays: number;
}

export interface LedgerReport {
  account: Account;
  from: string;
  to: string;
  opening: number;
  rows: { postingDate: string; journalNo: string; memo: string; counter: string; debit: number; credit: number; balance: number }[];
  totalDebit: number;
  totalCredit: number;
  closing: number;
  excludedCount: number;
}

export interface TrialReport {
  from: string;
  to: string;
  rows: { code: string; name: string; kind: string; kindLabel: string; opening: number; debit: number; credit: number; closing: number }[];
  totalDebit: number;
  totalCredit: number;
  balanced: boolean;
  excludedCount: number;
}

export interface BatchResult {
  no: string;
  ok: boolean;
  error: string;
}

export const STATUS_TONE: Record<JournalStatus, "muted" | "info" | "ok" | "bad"> = { draft: "muted", requested: "info", approved: "ok", canceled: "bad" };
export const KIND_CHOICES: [string, string][] = [
  ["", "전체"],
  ["general", "일반"],
  ["auto_purchase", "자동 매입"],
  ["auto_expense", "자동 비용"],
  ["auto_aicost", "자동 AI 비용"],
  ["reversal", "역분개"],
  ["opening", "기초"],
];
export const STATUS_CHOICES: [string, string][] = [
  ["", "전체"],
  ["draft", "임시"],
  ["requested", "승인 요청"],
  ["approved", "승인"],
  ["canceled", "취소"],
];
