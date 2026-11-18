// 전자세금계산서(B43) 화면의 서버 응답 형태와 한국어 표시 이름. 영문 상태 코드는 화면에 보이지 않는다(이름은 서버가 함께 준다).
import type { Tone } from "../layout/ui";

export type TaxStatus = "draft" | "issued" | "sent" | "accepted" | "failed" | "canceled";

export interface TaxParty {
  bizNo: string;
  branchNo: string;
  name: string;
  rep: string;
  addr: string;
  email: string;
}

export interface TaxLine {
  seq?: number;
  day: string;
  name: string;
  spec: string;
  qty: number;
  unitPrice: number;
  supply: number;
  tax?: number;
  remark: string;
}

export interface TaxTransmission {
  attemptNo: number;
  at: string;
  kind: string;
  result: string;
  httpStatus: number;
}

export interface TaxInvoice {
  /** 상대방 사업자번호가 그룹의 다른 법인과 같을 때 그 법인 이름(법인 간 거래 배지, B44). 아니면 빠진다. */
  intercompany?: string;
  id: number;
  no: string;
  direction: "sale" | "purchase";
  directionName: string;
  taxType: "taxable" | "zero" | "exempt";
  taxTypeName: string;
  status: TaxStatus;
  statusName: string;
  approvalNo: string;
  issueDate: string;
  supplier: TaxParty;
  buyer: TaxParty;
  supplyTotal: number;
  taxTotal: number;
  total: number;
  amendOfId: number;
  amendOfNo: string;
  amendedByNo: string;
  amendReason: string;
  amendReasonName: string;
  chargeType: "receipt" | "claim";
  remark: string;
  source: string;
  attempts: number;
  resends: number;
  lastCodeText: string;
  issuedAt: string;
  sentAt: string;
  acceptedAt: string;
  canceledAt: string;
  cancelReason: string;
  matchStatus?: string;
  matchResult?: string;
  lines?: TaxLine[];
  transmissions?: TaxTransmission[];
  warnings?: string[];
}

export const TAX_STATUS_TONE: Record<TaxStatus, Tone> = { draft: "muted", issued: "info", sent: "info", accepted: "ok", failed: "bad", canceled: "muted" };

export const TAX_STATUS_CHOICES: [string, string][] = [
  ["", "전체"],
  ["draft", "작성 중"],
  ["issued", "발행 대기"],
  ["sent", "전송됨"],
  ["accepted", "국세청 수신"],
  ["failed", "실패"],
  ["canceled", "취소"],
];

export const TAX_TYPE_CHOICES: [string, string][] = [
  ["taxable", "과세"],
  ["zero", "영세율"],
  ["exempt", "면세"],
];

export const AMEND_REASON_CHOICES: [string, string][] = [
  ["entry_error", "기재사항 착오 정정"],
  ["amount_change", "공급가액 변동"],
  ["return", "환입"],
  ["contract_cancel", "계약 해제"],
  ["lc_after", "내국신용장 사후 개설"],
  ["double_issue", "착오에 의한 이중 발급"],
];
